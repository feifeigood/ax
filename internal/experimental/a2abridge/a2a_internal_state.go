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
	"encoding/json"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/ax/proto"
)

// The role used by the bridge as the source of internal_only state-marker messages.
const stateMarkerRole = "a2a_bridge"

// a2aStatePayload is the JSON shape of the internal_only state marker's
// TextContent payload. The marker is emitted as a Message with InternalOnly=true
// so it persists in the ExecutionEvents log (for the bridge to recover on resume)
// but is filtered out of the conversation log, the CLI display, and Gemini's view
// of conversation history.
type a2aStatePayload struct {
	ConversationID     string   `json:"conversation_id"`
	ActiveTaskID       string   `json:"active_task_id"`
	EmittedArtifactIDs []string `json:"emitted_artifact_ids,omitempty"`
}

func marshalA2AState(conversationID, activeTaskID string, emittedArtifactIDs []string) (*proto.Message, error) {
	payload := a2aStatePayload{
		ConversationID:     conversationID,
		ActiveTaskID:       activeTaskID,
		EmittedArtifactIDs: emittedArtifactIDs,
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal a2a state marker: %w", err)
	}
	return &proto.Message{
		Role:         stateMarkerRole,
		InternalOnly: true,
		Content: &proto.Content{
			Type: &proto.Content_Text{
				Text: &proto.TextContent{Text: string(bytes)},
			},
		},
	}, nil
}

// RecoverA2AState scans the message history for the most recent A2A state
// marker and returns its conversation_id, active_task_id, and the list
// of artifact IDs already emitted by the prior bridge invocation. The
// most recent marker wins so successive resume cycles see the latest state.
func RecoverA2AState(msgs []*proto.Message) (conversationID, activeTaskID string, emittedArtifactIDs []string, found bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if !isStateMarkerMessage(msgs[i]) {
			continue
		}
		t := msgs[i].GetContent().GetText()
		if t == nil {
			continue
		}
		var payload a2aStatePayload
		if err := json.Unmarshal([]byte(t.Text), &payload); err != nil {
			continue
		}
		return payload.ConversationID, payload.ActiveTaskID, payload.EmittedArtifactIDs, true
	}
	return "", "", nil, false
}

func emittedArtifactKeys(m map[a2a.ArtifactID]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, string(id))
	}
	return out
}

func isStateMarkerMessage(msg *proto.Message) bool {
	if msg == nil || !msg.GetInternalOnly() || msg.GetRole() != stateMarkerRole {
		return false
	}
	return msg.GetContent().GetText() != nil
}
