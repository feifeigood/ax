// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package browser

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newMockClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func TestWebFetcherFetch(t *testing.T) {
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.String(), "https://example.test/path"; got != want {
			t.Fatalf("url mismatch: got %q want %q", got, want)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("<html><body>Hello world</body></html>")),
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		}, nil
	})

	f := newWebFetcher(client)
	page, err := f.Fetch(context.Background(), Query{URL: "https://example.test/path"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got, want := page.StatusCode, http.StatusOK; got != want {
		t.Fatalf("status mismatch: got %d want %d", got, want)
	}
	if !strings.Contains(page.Body, "Hello world") {
		t.Fatalf("body mismatch: %q", page.Body)
	}
	if strings.Contains(page.Body, "<") {
		t.Fatalf("expected text-only body, got %q", page.Body)
	}
}
