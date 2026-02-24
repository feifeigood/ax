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

package controller

import (
	"testing"

	"github.com/google/gar/proto"
)

func TestCheckCommandApproval(t *testing.T) {
	question := `Can I execute "ls -la"?`

	tests := []struct {
		name         string
		history      []*proto.Content
		wantCont     bool
		wantErr      bool
		wantPrompted bool // whether handler should be called with a confirmation prompt
	}{
		{
			name:         "empty history prompts user",
			history:      nil,
			wantCont:     false,
			wantErr:      false,
			wantPrompted: true,
		},
		{
			name: "no matching confirmation prompts user",
			history: []*proto.Content{
				{Role: "user", Content: &proto.Content_Text{Text: &proto.TextContent{Text: "hello"}}},
				{Role: "assistant", Content: &proto.Content_Text{Text: &proto.TextContent{Text: "hi"}}},
			},
			wantCont:     false,
			wantErr:      false,
			wantPrompted: true,
		},
		{
			name: "broken history",
			history: []*proto.Content{
				{Role: "user", Content: &proto.Content_Text{Text: &proto.TextContent{Text: "hello"}}},
				{Role: "assistant", Content: &proto.Content_Text{Text: &proto.TextContent{Text: "hi"}}},
				{
					Role: "user",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id: "conf-1",
							Decision: &proto.ConfirmationContent_Approval{
								Approval: &proto.ApprovalDecision{Approved: true},
							},
						},
					},
				},
			},
			wantCont:     false,
			wantErr:      false,
			wantPrompted: true,
		},
		{
			name: "matching confirmation with approval continues",
			history: []*proto.Content{
				{
					Role: "assistant",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id:       "conf-1",
							Question: question,
						},
					},
				},
				{
					Role: "user",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id: "conf-1",
							Decision: &proto.ConfirmationContent_Approval{
								Approval: &proto.ApprovalDecision{Approved: true},
							},
						},
					},
				},
			},
			wantCont:     true,
			wantErr:      false,
			wantPrompted: false,
		},
		{
			name: "matching confirmation with decline returns error",
			history: []*proto.Content{
				{
					Role: "assistant",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id:       "conf-2",
							Question: question,
						},
					},
				},
				{
					Role: "user",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id: "conf-2",
							Decision: &proto.ConfirmationContent_Decline{
								Decline: &proto.DeclineDecision{Declined: true},
							},
						},
					},
				},
			},
			wantCont:     false,
			wantErr:      true,
			wantPrompted: false,
		},
		{
			name: "confirmation for different question prompts user",
			history: []*proto.Content{
				{
					Role: "assistant",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id:       "conf-3",
							Question: `Can I execute "rm -rf /"?`,
						},
					},
				},
				{
					Role: "user",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id: "conf-3",
							Decision: &proto.ConfirmationContent_Approval{
								Approval: &proto.ApprovalDecision{Approved: true},
							},
						},
					},
				},
			},
			wantCont:     false,
			wantErr:      false,
			wantPrompted: true,
		},
		{
			name: "confirmation asked but no response yet prompts user",
			history: []*proto.Content{
				{
					Role: "assistant",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id:       "conf-4",
							Question: question,
						},
					},
				},
			},
			wantCont:     false,
			wantErr:      false,
			wantPrompted: true,
		},
		{
			name: "response with mismatched ID prompts user again",
			history: []*proto.Content{
				{
					Role: "assistant",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id:       "conf-5",
							Question: question,
						},
					},
				},
				{
					Role: "user",
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id: "wrong-id",
							Decision: &proto.ConfirmationContent_Approval{
								Approval: &proto.ApprovalDecision{Approved: true},
							},
						},
					},
				},
			},
			wantCont:     false,
			wantErr:      false,
			wantPrompted: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var prompted bool
			handler := func(resp *proto.ProcessResponse) error {
				prompted = true
				// Verify the prompt contains a confirmation with the question
				if len(resp.Contents) != 1 {
					t.Fatalf("expected 1 content in prompt, got %d", len(resp.Contents))
				}
				conf := resp.Contents[0].GetConfirmation()
				if conf == nil {
					t.Fatal("expected confirmation content in prompt")
				}
				if conf.Question != question {
					t.Errorf("expected question %q, got %q", question, conf.Question)
				}
				if conf.Id == "" {
					t.Error("expected non-empty confirmation ID")
				}
				return nil
			}

			cont, err := checkCommandApproval(tc.history, question, handler)

			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if cont != tc.wantCont {
				t.Errorf("expected cont=%v, got %v", tc.wantCont, cont)
			}
			if prompted != tc.wantPrompted {
				t.Errorf("expected prompted=%v, got %v", tc.wantPrompted, prompted)
			}
		})
	}
}
