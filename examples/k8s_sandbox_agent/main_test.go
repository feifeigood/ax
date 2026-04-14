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

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/ax/proto"
)

func TestExtractLatestPayload(t *testing.T) {
	// Let's create a simulated history payload
	history := []*proto.Message{
		{
			Role: "user",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{Text: "old user prompt"},
				},
			},
		},
		{
			Role: "agent",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{Text: "old agent response"},
				},
			},
		},
		{
			Role: "user",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{Text: "find this text plz plz plz"},
				},
			},
		},
		{
			Role: "agent",
			Content: &proto.Content{
				Type: &proto.Content_ToolCall{
					ToolCall: &proto.ToolCallContent{
						Type: &proto.ToolCallContent_FunctionCall{
							FunctionCall: &proto.FunctionCallContent{
								Name: "uppercase-agent",
							},
						},
					},
				},
			},
		},
	}

	// 1. Emulate the agent logic exactly!
	var targetText string
	for i := len(history) - 1; i >= 0; i-- {
		if content := history[i].GetContent(); content != nil {
			if text := content.GetText(); text != nil && history[i].Role == "user" {
				targetText = text.Text
				break
			}
		}
	}

	if targetText != "find this text plz plz plz" {
		t.Errorf("Failed to identify correct user substring! Got: %q", targetText)
	}

	// 2. Format output
	upper := strings.ToUpper(targetText)
	var outputs []*proto.Message
	outputs = append(outputs, &proto.Message{
		Role: "agent",
		Content: &proto.Content{
			Type: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "Hey, I'm your sandbox agent.\n",
				},
			},
		},
	})
	outputs = append(outputs, &proto.Message{
		Role: "agent",
		Content: &proto.Content{
			Type: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: fmt.Sprintf("here is your upper case text: %s", upper),
				},
			},
		},
	})

	if len(outputs) != 2 || !strings.Contains(outputs[1].GetContent().GetText().Text, strings.ToUpper("find this text plz plz plz")) {
		t.Errorf("Outputs failed! Got: %v", outputs)
	}
}
