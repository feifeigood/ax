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
	"context"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/google/ax/internal/config"
	"github.com/google/ax/internal/controller/executor"
	"github.com/google/ax/proto"
	"github.com/spf13/cobra"
)

// Data structures
type Text struct {
	Text string `json:"text"`
}

type Approval struct {
	Approved bool `json:"approved"`
}

type Confirmation struct {
	ID       string    `json:"id"`
	Question string    `json:"question,omitempty"`
	Approval *Approval `json:"approval,omitempty"`
}

type Content struct {
	Role         string        `json:"role"`
	Text         *Text         `json:"text,omitempty"`
	Confirmation *Confirmation `json:"confirmation,omitempty"`
}

type ExecutionEvent struct {
	ExecID    string    `json:"exec_id"`
	AgentID   string    `json:"agent_id"`
	Inputs    []Content `json:"inputs"`
	Outputs   []Content `json:"outputs"`
	State     string    `json:"state"`
	Timestamp time.Time `json:"timestamp"`
}

type ExecTrace struct {
	ExecID  string           `json:"exec_id"`
	AgentID string           `json:"agent_id"`
	Events  []ExecutionEvent `json:"events"`
}

type TraceData struct {
	RootExecID string      `json:"root_exec_id"`
	Execs      []ExecTrace `json:"execs"`
}

var (
	conversationID string
	traceServerAddr string
	traceConfigFile string
)

var traceCmd = &cobra.Command{
	Use:   "trace",
	Short: "View the execution trace",
	RunE:  runTrace,
}

func init() {
	traceCmd.Flags().StringVar(&conversationID, "conversation", "", "Conversation ID")
	traceCmd.Flags().StringVar(&traceServerAddr, "addr", "localhost:8080", "Server address to listen on")
	traceCmd.Flags().StringVar(&traceConfigFile, "config", "ax.yaml", "Path to YAML configuration file")
	traceCmd.MarkFlagRequired("conversation")
}

func runTrace(cmd *cobra.Command, args []string) error {
	// Load trace data
	data, err := loadTraceData(cmd.Context(), conversationID)
	if err != nil {
		return fmt.Errorf("error loading trace data: %w", err)
	}

	if len(data.Execs) == 0 {
		return fmt.Errorf("no trace data found")
	}

	// Start HTTP server on specified address
	listener, err := net.Listen("tcp", traceServerAddr)
	if err != nil {
		return fmt.Errorf("failed to bind server (try another using --server): %w", err)
	}

	return serveTraceUI(listener, data, indexHTML)
}

func loadTraceData(ctx context.Context, convID string) (*TraceData, error) {
	// The trace command uses the config provided by --config flag
	configPath := traceConfigFile

	events, rootExecID, err := fetchEventsByConversation(ctx, configPath, convID)
	if err != nil {
		return nil, err
	}

	data := &TraceData{
		RootExecID: rootExecID,
		Execs:      buildExecTraces(rootExecID, events),
	}

	return data, nil
}



func fetchEventsByConversation(ctx context.Context, configPath string, convID string) ([]*proto.ExecutionEvent, string, error) {
	cfg, err := config.LoadFromFile(configPath)
	if err != nil {
		return nil, "", fmt.Errorf("error loading config: %w", err)
	}

	evLog, err := executor.OpenSQLiteEventLog(cfg.EventLog.SQLiteConfig.Filename)
	if err != nil {
		return nil, "", fmt.Errorf("could not open sqlite eventlog: %w", err)
	}
	defer evLog.Close()

	convEvents, err := evLog.Events(ctx, convID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to query conversation events: %w", err)
	}

	var execIDs []string
	seen := make(map[string]bool)
	for _, ev := range convEvents {
		if ev.ExecId != "" && !seen[ev.ExecId] {
			execIDs = append(execIDs, ev.ExecId)
			seen[ev.ExecId] = true
		}
	}

	if len(execIDs) == 0 {
		return nil, "", fmt.Errorf("no executions found for conversation: %s", convID)
	}

	var allEvents []*proto.ExecutionEvent
	for _, eID := range execIDs {
		events, err := evLog.ExecEvents(ctx, eID)
		if err != nil {
			return nil, "", fmt.Errorf("failed to query events for exec %s: %w", eID, err)
		}
		allEvents = append(allEvents, events...)
	}

	// Use the first execID as the rootExecID as requested by user
	rootExecID := execIDs[0]

	return allEvents, rootExecID, nil
}

func buildExecTraces(rootExecID string, events []*proto.ExecutionEvent) []ExecTrace {
	execsMap := make(map[string][]ExecutionEvent)

	for _, protoEv := range events {
		exID := protoEv.ExecId
		ev := extractExecutionEvent(exID, protoEv)
		execsMap[exID] = append(execsMap[exID], ev)
	}

	var execs []ExecTrace
	for execID, evs := range execsMap {
		agentID := ""
		for _, ev := range evs {
			if ev.AgentID != "" {
				agentID = ev.AgentID
				break
			}
		}
		execs = append(execs, ExecTrace{
			ExecID:  execID,
			AgentID: agentID,
			Events:  evs,
		})
	}

	// Root exec first, then sub-execs sorted by name.
	sort.Slice(execs, func(i, j int) bool {
		if execs[i].ExecID == rootExecID {
			return true
		}
		if execs[j].ExecID == rootExecID {
			return false
		}
		return execs[i].ExecID < execs[j].ExecID
	})

	return execs
}

func extractMsgs(protoContents []*proto.Message) []Content {
	var results []Content
	for _, c := range protoContents {
		content := Content{Role: c.Role}
		msgContent := c.GetContent()
		if msgContent == nil {
			continue
		}
		if textC := msgContent.GetText(); textC != nil {
			content.Text = &Text{Text: textC.Text}
		} else if conf := msgContent.GetConfirmation(); conf != nil {
			content.Confirmation = &Confirmation{
				ID:       conf.Id,
				Question: conf.Question,
			}
			if app := conf.GetApproval(); app != nil {
				content.Confirmation.Approval = &Approval{Approved: app.Approved}
			} else if dec := conf.GetDecline(); dec != nil {
				content.Confirmation.Approval = &Approval{Approved: !dec.Declined}
			}
		}
		results = append(results, content)
	}
	return results
}

func extractExecutionEvent(execID string, protoEv *proto.ExecutionEvent) ExecutionEvent {
	ev := ExecutionEvent{
		ExecID:  execID,
		AgentID: protoEv.AgentId,
	}
	if protoEv.Timestamp != nil {
		ev.Timestamp = protoEv.Timestamp.AsTime()
	}

	ev.State = fmt.Sprint(protoEv.State)
	ev.Outputs = extractMsgs(protoEv.Outputs)
	ev.Inputs = extractMsgs(protoEv.Inputs)

	return ev
}
