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

// captureHandler records every AgentOutputs emission for later inspection.
type captureHandler struct {
	emissions []*proto.AgentOutputs
}

func (c *captureHandler) handle(out *proto.AgentOutputs) error {
	c.emissions = append(c.emissions, out)
	return nil
}

// allMessages returns the flattened list of messages across every emission.
func (c *captureHandler) allMessages() []*proto.Message {
	var out []*proto.Message
	for _, e := range c.emissions {
		out = append(out, e.Messages...)
	}
	return out
}

// findConfirmation returns the first ConfirmationContent emitted, or nil.
func (c *captureHandler) findConfirmation() *proto.ConfirmationContent {
	for _, msg := range c.allMessages() {
		if conf := msg.GetContent().GetConfirmation(); conf != nil {
			return conf
		}
	}
	return nil
}

// findStateMarker returns the recovered state marker fields, if any was emitted.
func (c *captureHandler) findStateMarker() (conv string, taskID string, ok bool) {
	conv, taskID, _, ok = RecoverA2AState(c.allMessages())
	return
}

func TestStreamState_TaskWithInputRequiredEmitsConfirmation(t *testing.T) {
	cap := &captureHandler{}
	const toolCallID = "call-abc"
	state := newStreamState("conv-1", toolCallID, "", cap.handle)

	question := "Save flask_server.py to /tmp/flask_server.py?"
	task := &a2a.Task{
		ID:        "task-2",
		ContextID: "conv-1",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateInputRequired,
			Message: &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: []*a2a.Part{a2a.NewTextPart(question)},
			},
		},
	}

	stop, err := state.handleEvent(task)
	if err != nil {
		t.Fatalf("handleEvent returned error: %v", err)
	}
	if !stop {
		t.Fatal("handleEvent: expected stop=true for INPUT_REQUIRED, got false")
	}

	conf := cap.findConfirmation()
	if conf == nil {
		t.Fatal("expected a ConfirmationContent emission, got none")
	}
	if conf.Id != toolCallID {
		t.Errorf("ConfirmationContent.Id: got %q, want %q (planner's ToolCall.Id)", conf.Id, toolCallID)
	}
	if !strings.Contains(conf.Question, question) {
		t.Errorf("ConfirmationContent.Question: got %q, want it to contain %q", conf.Question, question)
	}

	convID, taskID, ok := cap.findStateMarker()
	if !ok {
		t.Fatal("expected a state marker emission, got none")
	}
	if convID != "conv-1" {
		t.Errorf("state marker conversationID: got %q, want %q", convID, "conv-1")
	}
	if taskID != "task-2" {
		t.Errorf("state marker activeTaskID: got %q, want %q", taskID, "task-2")
	}
}

func TestStreamState_TaskWithCompletedDoesNotEmitConfirmation(t *testing.T) {
	// A completed Task event should mark terminal=true and emit any
	// artifacts but NOT emit a ConfirmationContent or stop the loop.
	cap := &captureHandler{}
	state := newStreamState("conv-1", "call-abc", "", cap.handle)

	task := &a2a.Task{
		ID:        "task-3",
		ContextID: "conv-1",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
		Artifacts: []*a2a.Artifact{
			{
				ID:    "a1",
				Name:  "result",
				Parts: []*a2a.Part{a2a.NewTextPart("done")},
			},
		},
	}

	stop, err := state.handleEvent(task)
	if err != nil {
		t.Fatalf("handleEvent returned error: %v", err)
	}
	if stop {
		t.Fatal("handleEvent: expected stop=false for COMPLETED task event, got true")
	}
	if !state.terminal {
		t.Error("expected state.terminal=true after a completed Task event")
	}
	if cap.findConfirmation() != nil {
		t.Error("did not expect a ConfirmationContent emission for a completed task")
	}
	// Artifact should have been emitted.
	if len(cap.allMessages()) == 0 {
		t.Error("expected the artifact to be emitted to the client")
	}
}

func TestStreamState_TaskStatusUpdateInputRequiredEmitsConfirmation(t *testing.T) {
	// The streaming path: INPUT_REQUIRED arrives as a
	// TaskStatusUpdateEvent. The bridge should emit a single
	// ConfirmationContent.
	cap := &captureHandler{}
	state := newStreamState("conv-1", "call-xyz", "", cap.handle)

	question := "Save flask_server.py?"
	evt := &a2a.TaskStatusUpdateEvent{
		ContextID: "conv-1",
		TaskID:    "task-2",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateInputRequired,
			Message: &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: []*a2a.Part{a2a.NewTextPart(question)},
			},
		},
	}

	stop, err := state.handleEvent(evt)
	if err != nil {
		t.Fatalf("handleEvent returned error: %v", err)
	}
	if !stop {
		t.Fatal("handleEvent: expected stop=true for INPUT_REQUIRED, got false")
	}

	conf := cap.findConfirmation()
	if conf == nil {
		t.Fatal("expected a ConfirmationContent emission, got none")
	}
	if !strings.Contains(conf.Question, question) {
		t.Errorf("ConfirmationContent.Question: got %q, want it to contain %q", conf.Question, question)
	}

	// Verify the question text was NOT also emitted as a separate
	// TextContent (the de-duplication fix from a prior round).
	textOnlyEmissions := 0
	for _, msg := range cap.allMessages() {
		if msg.GetContent().GetText() != nil && strings.Contains(msg.GetContent().GetText().Text, question) {
			textOnlyEmissions++
		}
	}
	if textOnlyEmissions > 0 {
		t.Errorf("INPUT_REQUIRED status message should NOT be emitted as a standalone TextContent (got %d such emissions)", textOnlyEmissions)
	}
}
