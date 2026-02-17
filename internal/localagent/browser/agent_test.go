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

	"github.com/google/gar/proto"
)

func testFetcher(t *testing.T) *webFetcher {
	t.Helper()
	client := newMockClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("<html><body>Sample content</body></html>")),
			Header:     http.Header{"Content-Type": []string{"text/html"}},
		}, nil
	})
	return newWebFetcher(client)
}

func newTestAgent(t *testing.T) *BrowserAgent {
	t.Helper()
	return &BrowserAgent{
		fetcher: testFetcher(t),
	}
}

func TestBrowserAgentProcess(t *testing.T) {
	a := newTestAgent(t)

	var chunks []string
	err := a.Process(context.Background(), "s1", &proto.ProcessRequest{
		CheckpointId: "cp1",
		Contents: []*proto.Content{
			{
				Role:    "user",
				Content: &proto.Content_Text{Text: &proto.TextContent{Text: "Check out this URL: example.test/page"}},
			},
		},
	}, func(out *proto.ProcessResponse) error {
		if out.CheckpointId == "" {
			t.Fatalf("checkpoint ID should not be empty")
		}
		if len(out.Contents) == 0 || out.Contents[0].GetText() == nil {
			t.Fatalf("missing text content")
		}
		chunks = append(chunks, out.Contents[0].GetText().Text)
		return nil
	})
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	last := chunks[0]
	if !strings.Contains(last, "Fetched: https://example.test/page") {
		t.Fatalf("missing fetched URL: %s", last)
	}
	if !strings.Contains(last, "Sample content") {
		t.Fatalf("missing page body: %s", last)
	}
	if strings.Contains(last, "<body>") || strings.Contains(last, "<html>") {
		t.Fatalf("expected html stripped: %s", last)
	}
}
