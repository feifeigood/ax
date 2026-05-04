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
	"testing"

	"github.com/google/ax/proto"
)

// buildStateMarker wraps marshalA2AState for tests; the inputs used here are
// always JSON-marshalable (string + []string), so an error fails the test.
func buildStateMarker(t *testing.T, conversationID, activeTaskID string, emittedArtifactIDs []string) *proto.Message {
	t.Helper()
	m, err := marshalA2AState(conversationID, activeTaskID, emittedArtifactIDs)
	if err != nil {
		t.Fatalf("marshalA2AState: %v", err)
	}
	return m
}

func TestStateMarkerRoundTrip(t *testing.T) {
	msg := buildStateMarker(t, "conv-abc", "task-xyz", []string{"art-1", "art-2"})
	if !isStateMarkerMessage(msg) {
		t.Fatal("isStateMarkerMessage: expected marker to be recognised")
	}
	conv, task, emitted, found := RecoverA2AState([]*proto.Message{msg})
	if !found {
		t.Fatal("RecoverA2AState: not found")
	}
	if conv != "conv-abc" {
		t.Errorf("conversationID: got %q, want %q", conv, "conv-abc")
	}
	if task != "task-xyz" {
		t.Errorf("activeTaskID: got %q, want %q", task, "task-xyz")
	}
	if !equalStrings(emitted, []string{"art-1", "art-2"}) {
		t.Errorf("emittedArtifactIDs: got %v, want [art-1 art-2]", emitted)
	}
}

func TestStateMarkerRoundTrip_NilEmittedArtifactIDs(t *testing.T) {
	msg := buildStateMarker(t, "conv", "task", nil)
	_, _, emitted, found := RecoverA2AState([]*proto.Message{msg})
	if !found {
		t.Fatal("RecoverA2AState: not found")
	}
	if emitted != nil {
		t.Errorf("emittedArtifactIDs: got %v, want nil", emitted)
	}
}

func TestRecoverA2AState_ReturnsMostRecentMarker(t *testing.T) {
	got := []*proto.Message{
		buildStateMarker(t, "conv-1", "task-1", []string{"art-old"}), // older
		modelTextMsg("did some work"),
		buildStateMarker(t, "conv-1", "", nil), // newer (clears the marker)
	}
	conv, task, emitted, found := RecoverA2AState(got)
	if !found {
		t.Fatal("RecoverA2AState: not found")
	}
	if conv != "conv-1" {
		t.Errorf("conversationID: got %q, want %q", conv, "conv-1")
	}
	if task != "" {
		t.Errorf("activeTaskID: got %q, want empty (cleared)", task)
	}
	if emitted != nil {
		t.Errorf("emittedArtifactIDs: got %v, want nil (most recent marker is cleared)", emitted)
	}
}

func TestIsStateMarkerMessage_RejectsForeignInternalOnlyText(t *testing.T) {
	foreign := &proto.Message{
		Role:         "future_subsystem",
		InternalOnly: true,
		Content: &proto.Content{
			Type: &proto.Content_Text{Text: &proto.TextContent{Text: "anything"}},
		},
	}
	if isStateMarkerMessage(foreign) {
		t.Error("expected isStateMarkerMessage to reject InternalOnly TextContent from non-bridge role")
	}
}
