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
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// Query represents a validated URL to fetch.
type Query struct {
	URL string
}

// PageContent contains the fetched page data including headers and converted text body.
type PageContent struct {
	URL         string // The final URL after any redirects
	StatusCode  int    // HTTP status code
	ContentType string // Content-Type header value
	Body        string // HTML converted to plain text
}

// maxFetchBytes limits how much data can be downloaded from a single page.
const maxFetchBytes = 256 * 1024

// webFetcher retrieves and converts web pages to text.
type webFetcher struct {
	client *http.Client
}

// newWebFetcher creates a new web fetcher with the given HTTP client.
// If client is nil, a default client with 15-second timeout is used.
func newWebFetcher(client *http.Client) *webFetcher {
	if client == nil {
		return &webFetcher{client: &http.Client{Timeout: 15 * time.Second}}
	}
	return &webFetcher{client: client}
}

// Fetch retrieves the page at the given URL and converts HTML to plain text.
// It respects the context for cancellation and enforces a maxFetchBytes limit.
func (f *webFetcher) Fetch(ctx context.Context, q Query) (PageContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.URL, nil)
	if err != nil {
		return PageContent{}, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return PageContent{}, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return PageContent{}, err
	}

	markdown, err := htmltomarkdown.ConvertString(string(bodyBytes))
	if err != nil {
		return PageContent{}, err
	}

	return PageContent{
		URL:         q.URL,
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        strings.TrimSpace(markdown),
	}, nil
}
