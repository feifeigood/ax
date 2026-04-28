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
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.See
// the License for the specific language governing permissions and limitations
// under the License.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/ax/internal/agent"
	"github.com/google/ax/proto"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"log"
)

// AntigravityPlannerConfig configures the Antigravity-based planner.
type AntigravityPlannerConfig struct {
	Endpoint string // ws://localhost:8765 or similar
}

// AntigravityPlannerAgent implements the agent.Agent interface by calling an external Python server via WebSocket.
type AntigravityPlannerAgent struct {
	registry *Registry
	config   AntigravityPlannerConfig
	wsConn   *websocket.Conn
}

// NewAntigravityPlannerAgent creates a new Antigravity-based planner agent.
func NewAntigravityPlannerAgent(_ context.Context, registry *Registry, cfg AntigravityPlannerConfig) (agent.Agent, error) {
	return &AntigravityPlannerAgent{
		registry: registry,
		config:   cfg,
	}, nil
}

// Connect starts the agent loop.
func (p *AntigravityPlannerAgent) Connect(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) error {
	// TODO(lhuan): remove when stable
	log.Printf("[AX] Connecting to Antigravity WebSocket for execution %s", execID)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, p.config.Endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to dial WebSocket: %w", err)
	}
	defer conn.Close() // Ensure connection is closed when function exits

	// Create local message channel and read goroutine
	msgCh := make(chan []byte, 10)
	go func() {
		defer close(msgCh)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				// Exit when connection closed or read error occurs
				return
			}
			msgCh <- message
		}
	}()

	return p.loop(ctx, conversationID, execID, start, e, handler, conn, msgCh)
}

// loop is the main execution loop for the Antigravity planner.
func (p *AntigravityPlannerAgent) loop(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler, conn *websocket.Conn, msgCh <-chan []byte) error {

	agentMsg := &proto.AgentMessage{
		Type: &proto.AgentMessage_Start{
			Start: start,
		},
	}

	payload, err := protojson.Marshal(agentMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal AgentMessage: %w", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return fmt.Errorf("failed to send request over WebSocket: %w", err)
	}

	for {
		nextAgentID, keepLooping, err := p.process(ctx, conn, msgCh, handler)
		if err != nil {
			return err
		}
		if !keepLooping && nextAgentID == "" {
			return nil
		}

		if nextAgentID != "" {
			startMsg := &proto.AgentStart{
				AgentId:  nextAgentID,
				Messages: start.Messages,
			}

			var toolOutputs []*proto.Message
			outputCapturer := func(resp *proto.AgentOutputs) error {
				toolOutputs = append(toolOutputs, resp.Messages...)
				return handler(resp)
			}

			if _, err := e.Exec(ctx, conversationID, nextAgentID, startMsg, outputCapturer); err != nil {
				log.Printf("Failed to execute tool via controller: %v", err)
			}
			// TODO: remove this log later
			log.Printf("[AX] Captured %d tool outputs for %s", len(toolOutputs), nextAgentID)

			// Capture output and send back to Python
			outputsMsg := &proto.AgentMessage{
				Type: &proto.AgentMessage_Outputs{
					Outputs: &proto.AgentOutputs{
						Messages: toolOutputs,
					},
				},
			}
			payload, err := protojson.Marshal(outputsMsg)
			if err != nil {
				log.Printf("Failed to marshal tool outputs: %v", err)
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				log.Printf("Failed to send tool outputs over WebSocket: %v", err)
				return fmt.Errorf("failed to send tool outputs: %w", err)
			}
		}
	}
}

// HealthCheck implements the agent.Agent interface.
func (p *AntigravityPlannerAgent) HealthCheck(ctx context.Context) error {
	return nil
}

// Close implements the agent.Agent interface.
func (p *AntigravityPlannerAgent) Close() error {
	return nil
}

func (p *AntigravityPlannerAgent) process(ctx context.Context, conn *websocket.Conn, msgCh <-chan []byte, handler agent.OutputHandler) (string, bool, error) {
	for {
		var message []byte
		var ok bool
		select {
		case message, ok = <-msgCh:
			if !ok {
				return "", false, fmt.Errorf("WebSocket channel closed")
			}
		case <-ctx.Done():
			return "", false, ctx.Err()
		}


		// Check for custom confirmation message
		var customMsg struct {
			Confirmation struct {
				Id       string `json:"id"`
				Question string `json:"question"`
			} `json:"confirmation"`
		}
		if err := json.Unmarshal(message, &customMsg); err == nil && customMsg.Confirmation.Id != "" {
			log.Printf("[AX] Received confirmation request for tool: %s", customMsg.Confirmation.Id)
			outputs := &proto.AgentOutputs{
				Messages: []*proto.Message{
					{
						Content: &proto.Content{
							Type: &proto.Content_Confirmation{
								Confirmation: &proto.ConfirmationContent{
									Id:       customMsg.Confirmation.Id,
									Question: customMsg.Confirmation.Question,
								},
							},
						},
					},
				},
			}
			handler(outputs)
			return "", false, nil
		}

		// Check for custom done message
		var doneMsg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(message, &doneMsg); err == nil && doneMsg.Type == "done" {
			log.Printf("[AX] Conversation completed.")
			return "", false, nil
		}

		var agentMsg proto.AgentMessage
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(message, &agentMsg); err != nil {
			log.Printf("Failed to unmarshal WebSocket message as AgentMessage: %v", err)
			continue
		}

		if outputs := agentMsg.GetOutputs(); outputs != nil {
			handler(outputs)

			for _, msg := range outputs.Messages {
				if msg.Content != nil {
					switch o := msg.Content.Type.(type) {
					case *proto.Content_Confirmation:
						// TODO(lhuan): still need to implement logic end to end
						log.Printf("[AX] Waiting for user response via client resumption...")
						return "", false, nil
					case *proto.Content_ToolCall:
						log.Printf("[AX] Handling tool call: %s", o.ToolCall.GetFunctionCall().GetName())
						nextAgentID := o.ToolCall.GetFunctionCall().GetName()
						return nextAgentID, false, nil
					}
				}
			}
			continue
		}

		if agentMsg.GetEnd() != nil {
			log.Printf("[AX] Conversation completed.")
			return "", false, nil
		}
	}
}
