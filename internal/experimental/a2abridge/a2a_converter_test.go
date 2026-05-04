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

package a2abridge

import (
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/ax/proto"
)

// userTextMsg returns a user-role message wrapping the given text.
func userTextMsg(text string) *proto.Message {
	return &proto.Message{
		Role: "user",
		Content: &proto.Content{
			Type: &proto.Content_Text{Text: &proto.TextContent{Text: text}},
		},
	}
}

// modelTextMsg returns a model-role message wrapping the given text.
func modelTextMsg(text string) *proto.Message {
	return &proto.Message{
		Role: "model",
		Content: &proto.Content{
			Type: &proto.Content_Text{Text: &proto.TextContent{Text: text}},
		},
	}
}

// modelDocument returns a model-role message wrapping a DocumentContent
// for the supplied MIME string.
func modelDocument(data []byte, mime, name string) *proto.Message {
	_ = name
	e := documentMimeFromString[mime] // zero value = TYPE_UNSPECIFIED
	return &proto.Message{
		Role: "model",
		Content: &proto.Content{
			Type: &proto.Content_Document{
				Document: &proto.DocumentContent{
					MimeType:  e,
					DataOrUri: &proto.DocumentContent_Data{Data: data},
				},
			},
		},
	}
}

// userConfirmation returns a user-role ConfirmationContent reply.
func userConfirmation(id string, approved bool) *proto.Message {
	conf := &proto.ConfirmationContent{Id: id}
	if approved {
		conf.Decision = &proto.ConfirmationContent_Approval{
			Approval: &proto.ApprovalDecision{Approved: true},
		}
	} else {
		conf.Decision = &proto.ConfirmationContent_Decline{
			Decline: &proto.DeclineDecision{Declined: true},
		}
	}
	return &proto.Message{
		Role:    "user",
		Content: &proto.Content{Type: &proto.Content_Confirmation{Confirmation: conf}},
	}
}

// modelConfirmation returns a model-role ConfirmationContent question.
func modelConfirmation(id, question string) *proto.Message {
	return &proto.Message{
		Role: "model",
		Content: &proto.Content{
			Type: &proto.Content_Confirmation{
				Confirmation: &proto.ConfirmationContent{Id: id, Question: question},
			},
		},
	}
}

// modelToolCall returns a model-role ToolCall message.
func modelToolCall(id, name string) *proto.Message {
	return &proto.Message{
		Role: "model",
		Content: &proto.Content{
			Type: &proto.Content_ToolCall{
				ToolCall: &proto.ToolCallContent{
					Id: id,
					Type: &proto.ToolCallContent_FunctionCall{
						FunctionCall: &proto.FunctionCallContent{Name: name},
					},
				},
			},
		},
	}
}

// stateMarker returns the kind of internal_only marker the bridge
// persists during HITL. Delegates to buildStateMarker so a marshalA2AState
// failure aborts the test rather than silently producing nil.
func stateMarker(t *testing.T, conversationID, taskID string) *proto.Message {
	t.Helper()
	return buildStateMarker(t, conversationID, taskID, nil)
}

// partTexts extracts the Text() value of every Text-typed Part in order,
// for easy comparison in test assertions.
func partTexts(parts []*a2a.Part) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == nil {
			continue
		}
		if t := p.Text(); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func TestLatestUserInputParts_SingleUserMessage(t *testing.T) {
	got := LatestUserInputParts([]*proto.Message{
		userTextMsg("hello world"),
	})
	want := []string{"hello world"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, want) {
		t.Fatalf("LatestUserInputParts: got %v, want %v", gotTexts, want)
	}
}

func TestLatestUserInputParts_StopsAtPriorModelTurn(t *testing.T) {
	// Only the trailing user-role messages after the last model turn
	// should be returned.
	got := LatestUserInputParts([]*proto.Message{
		userTextMsg("write me a flask server"),    // exec 1 user input
		modelTextMsg("here is your flask server"), // exec 1 model reply
		userTextMsg("change port to 5050"),        // exec 2 user input
	})
	want := []string{"change port to 5050"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, want) {
		t.Fatalf("LatestUserInputParts: got %v, want %v", gotTexts, want)
	}
}

func TestLatestUserInputParts_StopsAtPriorModelDocument(t *testing.T) {
	// A DocumentContent emitted by the bridge (file artifact) is a
	// model-role content turn. The walk must stop there so we don't
	// re-send the prior text user message.
	got := LatestUserInputParts([]*proto.Message{
		userTextMsg("write me a flask server"),
		modelDocument([]byte("print('hi')"), "text/x-python", "hi.py"),
		userTextMsg("change port to 5050"),
	})
	want := []string{"change port to 5050"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, want) {
		t.Fatalf("LatestUserInputParts: got %v, want %v", gotTexts, want)
	}
}

func TestLatestUserInputParts_SkipsStateMarkers(t *testing.T) {
	// State markers should be transparent - they don't break the
	// contiguous-user run, but they don't contribute parts either.
	got := LatestUserInputParts([]*proto.Message{
		modelTextMsg("prior agent reply"),
		stateMarker(t, "conv-1", ""),
		userTextMsg("new user message"),
	})
	want := []string{"new user message"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, want) {
		t.Fatalf("LatestUserInputParts: got %v, want %v", gotTexts, want)
	}
}

func TestLatestUserInputParts_SkipsAXInternalContent(t *testing.T) {
	// Confirmation, ToolCall, ToolResult, Thought are AX-internal and
	// should not break the walk or contribute parts.
	got := LatestUserInputParts([]*proto.Message{
		modelTextMsg("prior turn"),
		modelConfirmation("task-x", "ok?"),
		userConfirmation("task-x", true),
		modelToolCall("call-1", "some-tool"),
		userTextMsg("the actual new request"),
	})
	want := []string{"the actual new request"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, want) {
		t.Fatalf("LatestUserInputParts: got %v, want %v", gotTexts, want)
	}
}

func TestLatestUserInputParts_MultipleConsecutiveUserMessages(t *testing.T) {
	// AX is one-message-per-exec in practice, so the helper returns
	// only the most recent user-typed message. If multiple user
	// messages somehow appear in a row, only the last one is returned.
	got := LatestUserInputParts([]*proto.Message{
		modelTextMsg("prior agent reply"),
		userTextMsg("first follow-up"),
		userTextMsg("second follow-up"),
	})
	want := []string{"second follow-up"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, want) {
		t.Fatalf("LatestUserInputParts: got %v, want %v", gotTexts, want)
	}
}

func TestLatestUserInputParts_SkipsPlannerOutputAfterUserInput(t *testing.T) {
	// AX's planner emits its own text reasoning and a ToolCall AFTER the
	// user's input but BEFORE the bridge is invoked. The helper must walk
	// past those model-role messages and still find the user's actual input
	// from earlier in the history.
	got := LatestUserInputParts([]*proto.Message{
		userTextMsg("write me a flask server"),
		modelTextMsg("here's a flask server"),
		modelDocument([]byte("..."), "text/x-python", "flask_app.py"),
		userTextMsg("change port to 5050"),            // the actual new input
		modelTextMsg("I'll delegate to coding-agent"), // planner reasoning
		modelToolCall("call-1", "coding-agent"),       // planner tool call
	})
	want := []string{"change port to 5050"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, want) {
		t.Fatalf("LatestUserInputParts (planner-after-user): got %v, want %v", gotTexts, want)
	}
}

func TestLatestUserInputParts_NoUserMessage(t *testing.T) {
	// History with only model content yields no parts.
	got := LatestUserInputParts([]*proto.Message{
		modelTextMsg("just an agent reply"),
	})
	if len(got) != 0 {
		t.Fatalf("LatestUserInputParts: got %d parts, want 0", len(got))
	}
}

func TestLatestUserInputParts_EmptyHistory(t *testing.T) {
	got := LatestUserInputParts(nil)
	if len(got) != 0 {
		t.Fatalf("LatestUserInputParts(nil): got %d parts, want 0", len(got))
	}
	got = LatestUserInputParts([]*proto.Message{})
	if len(got) != 0 {
		t.Fatalf("LatestUserInputParts(empty): got %d parts, want 0", len(got))
	}
}

func TestLatestUserInputParts_FollowupMessage(t *testing.T) {
	// End-to-end shape mimicking the user-reported bug: after exec 1
	// completes (HITL save approved + file artifact emitted), exec 2
	// kicks off with a new "change port" request. The bridge should
	// send only the new request to the A2A agent.
	got := LatestUserInputParts([]*proto.Message{
		userTextMsg("write me a flask server that listens on port 8080"),
		modelTextMsg("Here is the flask server: ..."),
		modelConfirmation("task-1", "I'd like to save this to ./flask_app.py"),
		stateMarker(t, "conv-1", "task-1"),
		userConfirmation("task-1", true),
		modelDocument([]byte("from flask import Flask\n..."), "text/x-python", "flask_app.py"),
		stateMarker(t, "conv-1", ""),
		userTextMsg("change port to 5050"),
	})
	want := []string{"change port to 5050"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, want) {
		t.Fatalf("LatestUserInputParts (exec 2 scenario): got %v, want %v", gotTexts, want)
	}
}

func TestAxMessagesToA2AParts_FullHistory(t *testing.T) {
	// Stateless mode: every text/file/data part across the whole
	// history (regardless of role) should be included.
	history := []*proto.Message{
		userTextMsg("write me a flask server"),
		modelTextMsg("here is your flask server"),
		modelDocument([]byte("print('hi')"), "text/x-python", "hi.py"),
		userTextMsg("change port to 5050"),
	}
	got := MessagesToA2AParts(history)
	wantTexts := []string{
		"write me a flask server",
		"here is your flask server",
		"change port to 5050",
	}
	if gotTexts := partTexts(got); !equalStrings(gotTexts, wantTexts) {
		t.Errorf("MessagesToA2AParts text parts: got %v, want %v", gotTexts, wantTexts)
	}
	// 3 text parts + 1 document part = 4 total.
	if len(got) != 4 {
		t.Errorf("MessagesToA2AParts total parts: got %d, want 4", len(got))
	}
}

func TestAxMessagesToA2AParts_SkipsAXInternalAndStateMarkers(t *testing.T) {
	// State markers and AX-internal types must be filtered out.
	got := MessagesToA2AParts([]*proto.Message{
		userTextMsg("hi"),
		stateMarker(t, "conv-1", "task-1"),
		modelConfirmation("task-1", "ok?"),
		userConfirmation("task-1", true),
		modelToolCall("call-1", "tool"),
		userTextMsg("bye"),
	})
	wantTexts := []string{"hi", "bye"}
	gotTexts := partTexts(got)
	if !equalStrings(gotTexts, wantTexts) {
		t.Fatalf("MessagesToA2AParts: got %v, want %v", gotTexts, wantTexts)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ----- AgentMetadataFromCard -----

// cardWithSkills returns a minimal AgentCard populated with the supplied
// fields and one Skill carrying name+description+tags+examples.
func cardWithSkills(name, description, skillName, skillDesc string, tags, examples []string) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        name,
		Description: description,
		Skills: []a2a.AgentSkill{
			{
				Name:        skillName,
				Description: skillDesc,
				Tags:        tags,
				Examples:    examples,
			},
		},
	}
}

func TestAgentMetadataFromCard_NilCard(t *testing.T) {
	// Nil card: returns cfg values unchanged, no skills enrichment.
	name, desc := AgentMetadataFromCard(nil, "cfgName", "cfgDescription")
	if name != "cfgName" {
		t.Errorf("name: got %q, want %q", name, "cfgName")
	}
	if desc != "cfgDescription" {
		t.Errorf("description: got %q, want %q", desc, "cfgDescription")
	}
}

func TestAgentMetadataFromCard_CfgOverridesCard(t *testing.T) {
	// Both cfgName and cfgDescription are non-empty; they win over the
	// card values. (Description still gets skills enrichment though.)
	card := cardWithSkills("cardName", "cardDescription", "skill1", "Does X", nil, nil)
	name, desc := AgentMetadataFromCard(card, "cfgName", "cfgDescription")
	if name != "cfgName" {
		t.Errorf("name: got %q, want %q (cfg wins)", name, "cfgName")
	}
	if !strings.HasPrefix(desc, "cfgDescription.") {
		t.Errorf("description: got %q, want it to start with 'cfgDescription.' (cfg wins, then enriched)", desc)
	}
	if !strings.Contains(desc, "skill1: Does X") {
		t.Errorf("description: got %q, want it to contain skill metadata", desc)
	}
}

func TestAgentMetadataFromCard_FallsBackToCardWhenCfgEmpty(t *testing.T) {
	// Empty cfg -> falls back to card values.
	card := cardWithSkills("cardName", "cardDescription", "skill1", "Does X", nil, nil)
	name, desc := AgentMetadataFromCard(card, "", "")
	if name != "cardName" {
		t.Errorf("name: got %q, want %q (fallback to card)", name, "cardName")
	}
	if !strings.HasPrefix(desc, "cardDescription.") {
		t.Errorf("description: got %q, want it to start with 'cardDescription.' (fallback to card, then enriched)", desc)
	}
}

func TestAgentMetadataFromCard_NoSkillsLeavesDescriptionUnchanged(t *testing.T) {
	// Card with no Skills -> description NOT enriched (no Skills: header).
	card := &a2a.AgentCard{Name: "cardName", Description: "cardDescription"}
	_, desc := AgentMetadataFromCard(card, "", "")
	if desc != "cardDescription" {
		t.Errorf("description: got %q, want %q (no skills, no enrichment)", desc, "cardDescription")
	}
}

func TestAgentMetadataFromCard_FullEnrichmentFormat(t *testing.T) {
	// Card with skills + tags + examples produces the full markdown
	// format: terminating period normalized; skills bullet list with
	// "(tags: ...)" suffix and indented "Examples: ..." line.
	card := cardWithSkills(
		"agent",
		"A coding agent",
		"code",
		"Write Python",
		[]string{"dev", "python"},
		[]string{"fizzbuzz", "hello world"},
	)
	_, desc := AgentMetadataFromCard(card, "", "")
	expected := "A coding agent.\n\nSkills:\n- code: Write Python (tags: dev, python)\n  Examples: \"fizzbuzz\"; \"hello world\"\n"
	if desc != expected {
		t.Errorf("description format mismatch\ngot:\n%s\nwant:\n%s", desc, expected)
	}
}
