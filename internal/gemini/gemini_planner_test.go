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

package gemini

import (
	"testing"

	"github.com/google/ax/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtoToContents(t *testing.T) {
	inputs := []*proto.Message{
		{
			Role: "user",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{Text: "hello"},
				},
			},
		},
		{
			Role: "model",
			Content: &proto.Content{
				Type: &proto.Content_ToolCall{
					ToolCall: &proto.ToolCallContent{
						Id: "call-123",
						Type: &proto.ToolCallContent_FunctionCall{
							FunctionCall: &proto.FunctionCallContent{
								Name: "test_tool",
								Arguments: &structpb.Struct{
									Fields: map[string]*structpb.Value{
										"arg1": structpb.NewStringValue("val1"),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	contents := protoToContents(inputs)

	if len(contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(contents))
	}

	if contents[0].Role != "user" {
		t.Errorf("expected role user, got %s", contents[0].Role)
	}
	if contents[0].Parts[0].Text != "hello" {
		t.Errorf("expected text hello, got %s", contents[0].Parts[0].Text)
	}

	if contents[1].Role != "model" {
		t.Errorf("expected role model, got %s", contents[1].Role)
	}
	fc := contents[1].Parts[0].FunctionCall
	if fc == nil {
		t.Fatal("expected function call")
	}
	if fc.Name != "test_tool" {
		t.Errorf("expected function name test_tool, got %s", fc.Name)
	}
}

func TestHandleConfirmationAnswer(t *testing.T) {
	inputs := []*proto.Message{
		{
			Role: "model",
			Content: &proto.Content{
				Type: &proto.Content_ToolCall{
					ToolCall: &proto.ToolCallContent{
						Id: "call-123",
						Type: &proto.ToolCallContent_FunctionCall{
							FunctionCall: &proto.FunctionCallContent{
								Name: "bash",
								Arguments: &structpb.Struct{
									Fields: map[string]*structpb.Value{
										"command": structpb.NewStringValue("ls"),
									},
								},
							},
						},
					},
				},
			},
		},
		{
			Role: "user",
			Content: &proto.Content{
				Type: &proto.Content_Confirmation{
					Confirmation: &proto.ConfirmationContent{
						Id:       "call-123",
						Decision: &proto.ConfirmationContent_Approval{Approval: &proto.ApprovalDecision{Approved: true}},
					},
				},
			},
		},
	}

	p := &geminiPlannerAgent{
		bashTool: &BashTool{},
	}

	fc, approved := p.handleConfirmationAnswer(inputs)

	if fc == nil {
		t.Fatal("expected function call")
	}
	if fc.ID != "call-123" {
		t.Errorf("expected ID call-123, got %s", fc.ID)
	}
	if !approved {
		t.Error("expected approved to be true")
	}
}
