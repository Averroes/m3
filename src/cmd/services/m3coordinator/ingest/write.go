// Copyright (c) 2019 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package ingest

import (
	"context"
	"sync"

	"github.com/m3db/m3/src/cmd/services/m3coordinator/downsample"
	"github.com/m3db/m3/src/metrics/policy"
	"github.com/m3db/m3/src/query/models"
	"github.com/m3db/m3/src/query/storage"
	"github.com/m3db/m3/src/query/ts"
	xerrors "github.com/m3db/m3x/errors"
	xtime "github.com/m3db/m3x/time"
)

// DownsampleAndWriteIter is an interface that can be implemented to use
// the WriteBatch method.
type DownsampleAndWriteIter interface {
	Next() bool
	Current() (models.Tags, ts.Datapoints, xtime.Unit)
	Reset() error
	Error() error
}

// DownsamplerAndWriter is the interface for the downsamplerAndWriter which
// writes metrics to the downsampler as well as to storage in unaggregated form.
type DownsamplerAndWriter interface {
	Write(
		ctx context.Context,
		tags models.Tags,
		datapoints ts.Datapoints,
		unit xtime.Unit,
		overrides MappingAndStoragePoliciesOverrides,
	) error

	// TODO(rartoul): Batch interface should also support downsampling rules.
	WriteBatch(
		ctx context.Context,
		iter DownsampleAndWriteIter,
	) error

	Storage() storage.Storage
}

// MappingAndStoragePoliciesOverrides contains overrides for the downsampling mapping
// rules and storage policies for a given write.
type MappingAndStoragePoliciesOverrides struct {
	OverrideMappingRules bool
	MappingRules         []downsample.MappingRule

	OverrideStoragePolicies bool
	StoragePolicies         []policy.StoragePolicy
}

// downsamplerAndWriter encapsulates the logic for writing data to the downsampler,
// as well as in unaggregated form to storage.
type downsamplerAndWriter struct {
	store       storage.Storage
	downsampler downsample.Downsampler
}

// NewDownsamplerAndWriter creates a new downsampler and writer.
func NewDownsamplerAndWriter(
	store storage.Storage,
	downsampler downsample.Downsampler,
) DownsamplerAndWriter {
	return &downsamplerAndWriter{
		store:       store,
		downsampler: downsampler,
	}
}

func (d *downsamplerAndWriter) Write(
	ctx context.Context,
	tags models.Tags,
	datapoints ts.Datapoints,
	unit xtime.Unit,
	overrides MappingAndStoragePoliciesOverrides,
) error {
	var (
		downsamplerExists = d.downsampler != nil
		// If they didn't request the mapping rules to be overriden, then assume they want the default
		// ones.
		useDefaultMappingRules = !overrides.OverrideMappingRules
		// If they did try and override the mapping rules, make sure they've provided at least one.
		overrideMappingRules = overrides.OverrideMappingRules && len(overrides.MappingRules) > 0
		// Only downsample if the downsampler exists, and they either want to use the default mapping
		// rules, or they're trying to override the mapping rules and they've provided at least one
		// override to do so.
		shouldDownsample = downsamplerExists && (useDefaultMappingRules || overrideMappingRules)
	)
	if shouldDownsample {
		appender, err := d.downsampler.NewMetricsAppender()
		if err != nil {
			return err
		}

		for _, tag := range tags.Tags {
			appender.AddTag(tag.Name, tag.Value)
		}

		var appenderOpts downsample.SampleAppenderOptions
		if overrideMappingRules {
			appenderOpts = downsample.SampleAppenderOptions{
				Override: true,
				OverrideRules: downsample.SamplesAppenderOverrideRules{
					MappingRules: overrides.MappingRules,
				},
			}
		}

		samplesAppender, err := appender.SamplesAppender(appenderOpts)
		if err != nil {
			return err
		}

		for _, dp := range datapoints {
			err := samplesAppender.AppendGaugeSample(dp.Value)
			if err != nil {
				return err
			}
		}

		appender.Finalize()
	}

	var (
		storageExists             = d.store != nil
		useDefaultStoragePolicies = !overrides.OverrideStoragePolicies
	)
	if storageExists && useDefaultStoragePolicies {
		return d.store.Write(ctx, &storage.WriteQuery{
			Tags:       tags,
			Datapoints: datapoints,
			Unit:       unit,
			Attributes: storage.Attributes{
				MetricsType: storage.UnaggregatedMetricsType,
			},
		})
	}

	if storageExists {
		var (
			wg       sync.WaitGroup
			multiErr xerrors.MultiError
			errLock  sync.Mutex
		)

		for _, p := range overrides.StoragePolicies {
			wg.Add(1)
			// TODO(rartoul): Benchmark using a pooled worker pool here.
			go func(p policy.StoragePolicy) {
				err := d.store.Write(ctx, &storage.WriteQuery{
					Tags:       tags,
					Datapoints: datapoints,
					Unit:       unit,
					Attributes: storage.Attributes{
						// TODO(rartoul): Is this a good assumption?
						// Assume all overriden storage policies are for aggregated namespaces.
						MetricsType: storage.AggregatedMetricsType,
						Resolution:  p.Resolution().Window,
						Retention:   p.Retention().Duration(),
					},
				})
				if err != nil {
					errLock.Lock()
					multiErr = multiErr.Add(err)
					errLock.Unlock()
				}
				wg.Done()
			}(p)
		}

		return nil
	}

	return nil
}

func (d *downsamplerAndWriter) WriteBatch(
	ctx context.Context,
	iter DownsampleAndWriteIter,
) error {
	var (
		wg       = sync.WaitGroup{}
		multiErr xerrors.MultiError
		errLock  sync.Mutex
		addError = func(err error) {
			errLock.Lock()
			multiErr = multiErr.Add(err)
			errLock.Unlock()
		}
	)

	if d.store != nil {
		// Write unaggregated. Spin up all the background goroutines that make
		// network requests before we do the synchronous work of writing to the
		// downsampler.
		for iter.Next() {
			wg.Add(1)
			tags, datapoints, unit := iter.Current()
			go func() {
				err := d.store.Write(ctx, &storage.WriteQuery{
					Tags:       tags,
					Datapoints: datapoints,
					Unit:       unit,
					Attributes: storage.Attributes{
						MetricsType: storage.UnaggregatedMetricsType,
					},
				})
				if err != nil {
					addError(err)
				}
				wg.Done()
			}()
		}
	}

	// Iter does not need to be synchronized because even though we use it to spawn
	// many goroutines above, the iteration is always synchronous.
	resetErr := iter.Reset()
	if resetErr != nil {
		addError(resetErr)
	}

	if d.downsampler != nil && resetErr == nil {
		err := d.writeAggregatedBatch(iter)
		if err != nil {
			addError(err)
		}
	}

	wg.Wait()
	return multiErr.LastError()
}

func (d *downsamplerAndWriter) writeAggregatedBatch(
	iter DownsampleAndWriteIter,
) error {
	appender, err := d.downsampler.NewMetricsAppender()
	if err != nil {
		return err
	}

	var opts downsample.SampleAppenderOptions
	for iter.Next() {
		appender.Reset()
		tags, datapoints, _ := iter.Current()
		for _, tag := range tags.Tags {
			appender.AddTag(tag.Name, tag.Value)
		}

		samplesAppender, err := appender.SamplesAppender(opts)
		if err != nil {
			return err
		}

		for _, dp := range datapoints {
			err := samplesAppender.AppendGaugeSample(dp.Value)
			if err != nil {
				return err
			}
		}
	}
	appender.Finalize()

	if err := iter.Error(); err != nil {
		return err
	}

	return nil
}

func (d *downsamplerAndWriter) Storage() storage.Storage {
	return d.store
}