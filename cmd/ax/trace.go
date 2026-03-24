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

type TaskTrace struct {
	ExecID  string           `json:"exec_id"`
	AgentID string           `json:"agent_id"`
	Events  []ExecutionEvent `json:"events"`
}

type TraceData struct {
	RootExecID string      `json:"root_exec_id"`
	Tasks      []TaskTrace `json:"tasks"`
}

var (
	traceID         string
	traceServerAddr string
	traceConfigFile string
)

var traceCmd = &cobra.Command{
	Use:   "trace",
	Short: "View the execution trace",
	RunE:  runTrace,
}

func init() {
	traceCmd.Flags().StringVar(&traceID, "id", "", "Execution ID")
	traceCmd.Flags().StringVar(&traceServerAddr, "addr", "localhost:8080", "Server address to listen on")
	traceCmd.Flags().StringVar(&traceConfigFile, "config", "ax.yaml", "Path to YAML configuration file")
	traceCmd.MarkFlagRequired("id")
}

func runTrace(cmd *cobra.Command, args []string) error {
	// Load trace data
	data, err := loadTraceData(traceID)
	if err != nil {
		return fmt.Errorf("error loading trace data: %w", err)
	}

	if len(data.Tasks) == 0 {
		return fmt.Errorf("no trace data found for execution ID: %s", traceID)
	}

	// Start HTTP server on specified address
	listener, err := net.Listen("tcp", traceServerAddr)
	if err != nil {
		return fmt.Errorf("failed to bind server (try another using --server): %w", err)
	}

	return serveTraceUI(listener, data, indexHTML)
}

func loadTraceData(rootExecID string) (*TraceData, error) {
	// The trace command uses the config provided by --config flag
	configPath := traceConfigFile

	events, err := fetchEventsFromDB(configPath, rootExecID)
	if err != nil {
		return nil, err
	}

	data := &TraceData{
		RootExecID: rootExecID,
		Tasks:      buildTaskTraces(rootExecID, events),
	}

	return data, nil
}

func fetchEventsFromDB(configPath string, rootExecID string) ([]*proto.ExecutionEvent, error) {
	cfg, err := config.LoadFromFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}

	evLog, err := executor.OpenSQLiteEventLog(cfg.EventLog.SQLiteConfig.Filename)
	if err != nil {
		return nil, fmt.Errorf("could not open sqlite eventlog: %w", err)
	}
	defer evLog.Close()

	ctx := context.Background()
	events, err := evLog.EventsByPrefix(ctx, rootExecID)
	if err != nil {
		return nil, fmt.Errorf("failed to query trace events: %w", err)
	}

	return events, nil
}

func buildTaskTraces(rootExecID string, events []*proto.ExecutionEvent) []TaskTrace {
	tasksMap := make(map[string][]ExecutionEvent)

	for _, protoEv := range events {
		exID := protoEv.ExecId
		ev := extractExecutionEvent(exID, protoEv)
		tasksMap[exID] = append(tasksMap[exID], ev)
	}

	var tasks []TaskTrace
	for execID, evs := range tasksMap {
		agentID := ""
		for _, ev := range evs {
			if ev.AgentID != "" {
				agentID = ev.AgentID
				break
			}
		}
		tasks = append(tasks, TaskTrace{
			ExecID:  execID,
			AgentID: agentID,
			Events:  evs,
		})
	}

	// Root task first, then sub-tasks sorted by name.
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].ExecID == rootExecID {
			return true
		}
		if tasks[j].ExecID == rootExecID {
			return false
		}
		return tasks[i].ExecID < tasks[j].ExecID
	})

	return tasks
}

func extractContents(protoContents []*proto.Content) []Content {
	var results []Content
	for _, c := range protoContents {
		content := Content{Role: c.Role}
		if textC := c.GetText(); textC != nil {
			content.Text = &Text{Text: textC.Text}
		} else if conf := c.GetConfirmation(); conf != nil {
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
	ev.Outputs = extractContents(protoEv.Outputs)
	ev.Inputs = extractContents(protoEv.Inputs)

	return ev
}
