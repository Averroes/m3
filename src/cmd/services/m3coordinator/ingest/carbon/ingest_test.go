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

package ingestcarbon

import (
	"fmt"
	"testing"

	"github.com/m3db/m3/src/query/models"
	"github.com/stretchr/testify/require"
)

func TestIngesterHandleConn(t *testing.T) {

}

func TestGenerateTagsFromName(t *testing.T) {
	testCases := []struct {
		name         string
		expectedTags []models.Tag
		expectedErr  error
	}{
		{
			name: "foo",
			expectedTags: []models.Tag{
				{Name: []byte("__$0__"), Value: []byte("foo")},
			},
		},
		{
			name: "foo.bar.baz",
			expectedTags: []models.Tag{
				{Name: []byte("__$0__"), Value: []byte("foo")},
				{Name: []byte("__$1__"), Value: []byte("bar")},
				{Name: []byte("__$2__"), Value: []byte("baz")},
			},
		},
		{
			name: "foo.bar.baz.",
			expectedTags: []models.Tag{
				{Name: []byte("__$0__"), Value: []byte("foo")},
				{Name: []byte("__$1__"), Value: []byte("bar")},
				{Name: []byte("__$2__"), Value: []byte("baz")},
			},
		},
		{
			name:        "foo..bar..baz..",
			expectedErr: fmt.Errorf("carbon metric: foo..bar..baz.. has duplicate separator"),
		},
		{
			name:        "foo.bar.baz..",
			expectedErr: fmt.Errorf("carbon metric: foo.bar.baz.. has duplicate separator"),
		},
	}

	for _, tc := range testCases {
		tags, err := generateTagsFromName([]byte(tc.name))
		if tc.expectedErr != nil {
			require.Equal(t, tc.expectedErr, err)
		} else {
			require.NoError(t, err)
		}
		require.Equal(t, tc.expectedTags, tags.Tags)
	}
}
