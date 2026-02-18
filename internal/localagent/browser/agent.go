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

// Package browser provides an agent that fetches and converts web content to text.
package browser

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/gar/agent"
	"github.com/google/gar/proto"
	"github.com/google/uuid"
	"mvdan.cc/xurls/v2"
)

// BrowserAgent takes a URL and retrieves its web contents.
// It fetches HTML pages and converts them to plain text format.
type BrowserAgent struct {
	fetcher *webFetcher
}

// NewAgent creates a new browser agent with default HTTP client settings.
func NewAgent() *BrowserAgent {
	return &BrowserAgent{
		fetcher: newWebFetcher(nil),
	}
}

var _ agent.Agent = (*BrowserAgent)(nil)

// Process handles incoming requests containing URLs to fetch.
// It extracts the URL from the request, fetches the page content,
// and streams the result back through the handler.
func (a *BrowserAgent) Process(ctx context.Context, sessionID string, incoming *proto.ProcessRequest, handler agent.OutputHandler) error {
	text := readUserText(incoming)
	if text == "" {
		return a.emit(handler, "Please provide a URL to fetch")
	}

	q, err := parseNaturalText(text)
	if err != nil {
		return a.emit(handler, "Invalid URL: "+err.Error())
	}

	page, err := a.fetcher.Fetch(ctx, q)
	if err != nil {
		return a.emit(handler, "Failed to fetch: "+err.Error())
	}

	body := page.Body
	if body == "" {
		body = "(empty body)"
	}
	return a.emit(handler,
		fmt.Sprintf("Fetched: %s\nStatus: %d\nContent-Type: %s\n\n%s", page.URL, page.StatusCode, page.ContentType, body))
}

// HealthCheck verifies the agent is operational.
func (a *BrowserAgent) HealthCheck(ctx context.Context) error {
	return nil
}

// Close cleans up any resources held by the agent.
func (a *BrowserAgent) Close() error {
	return nil
}

// emit is a helper that sends a text response through the output handler.
// It generates a new UUID for each response checkpoint.
func (a *BrowserAgent) emit(handler agent.OutputHandler, text string) error {
	return handler(&proto.ProcessResponse{
		Role:         "assistant",
		CheckpointId: uuid.New().String(),
		Contents: []*proto.Content{
			{
				Content: &proto.Content_Text{Text: &proto.TextContent{Text: text}},
			},
		},
	})
}

// readUserText extracts the most recent text content from the request.
// It searches backwards through the contents to find the latest user message.
func readUserText(incoming *proto.ProcessRequest) string {
	for i := len(incoming.Contents) - 1; i >= 0; i-- {
		if c := incoming.Contents[i]; c != nil {
			if t := c.GetText(); t != nil {
				if txt := strings.TrimSpace(t.Text); txt != "" {
					return txt
				}
			}
		}
	}
	return ""
}

// parseNaturalText parses input into a URL-only browser query.
func parseNaturalText(input string) (Query, error) {
	relaxedRegex := xurls.Relaxed()
	relaxedURLs := relaxedRegex.FindAllString(input, -1)
	if len(relaxedURLs) > 0 {
		url := relaxedURLs[len(relaxedURLs)-1]
		if !strings.HasPrefix(url, "http") {
			url = "https://" + url
		}
		return Query{URL: url}, nil
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return Query{}, fmt.Errorf("empty request")
	}
	u, err := parseURL(input)
	if err != nil {
		return Query{}, err
	}
	return Query{URL: u}, nil
}

// parseURL validates and normalizes a URL string.
// It adds https:// scheme if missing and validates the URL format.
func parseURL(s string) (string, error) {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.ParseRequestURI(s)
	if err != nil {
		return "", fmt.Errorf("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	return u.String(), nil
}
