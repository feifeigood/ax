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
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/gar/agent"
	"github.com/google/gar/cmd/gar/internal"
	"github.com/google/gar/internal/config"
	"github.com/google/gar/internal/controller"
	"github.com/google/gar/proto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	execID  string
	execAgentID    string
	execInput      string
	execServerAddr string
	execConfigFile string
)

var execCmd = &cobra.Command{
	Use:   "exec",
	Short: "Execute a task or resume an existing one",
	Long: `Execute a new agentic task or resume an existing one.
If no ID is provided, a new UUID will be generated.`,
	RunE: runExec,
}

func init() {
	execCmd.Flags().StringVar(&execID, "id", "", "ID (optional, generates UUID if not provided)")
	execCmd.Flags().StringVar(&execAgentID, "agent", "", "Agent ID (optional, planner is used if not specified)")
	execCmd.Flags().StringVar(&execInput, "input", "", "Input message to send (optional)")
	execCmd.Flags().StringVar(&execServerAddr, "server", "", "gRPC controller server address (if specified, connects to remote server; otherwise runs with a local built-in GAR server)")
	execCmd.Flags().StringVar(&execConfigFile, "config", "gar.yaml", "Path to YAML configuration file (only used with a local built-in GAR server)")
}

// TODO(jbd): Add multimodal input flags, e.g. --input-image.

var (
	execController *controller.Controller
)

func runExec(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Generate UUID if no ID provided
	if execID == "" {
		execID = uuid.New().String()
	}

	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nReceived interrupt, shutting down...")
		if execController != nil {
			execController.Close()
		}
		cancel()
	}()

	return execLoop(ctx, execID, execAgentID, execInput)
}

func execLoop(ctx context.Context, id string, agentID string, input string) error {
	d := internal.NewDisplay(id)
	d.DisplayHeader()

	if input == "" {
		var err error
		input, err = d.PromptForInput()
		if err != nil {
			return err
		}
	}

	d.DisplayInput(input)
	history := []*proto.Content{
		{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: input,
				},
			},
		},
	}

	for {
		conf, outputs, err := runAutoExec(ctx, d, id, agentID, history)
		if err != nil {
			return err
		}
		history = append(history, outputs...)

		if conf != nil {
			for {
				approved, err := d.PromptForApproval(conf.Question)
				if err != nil {
					return err
				}
				var decision []*proto.Content
				if approved {
					decision = []*proto.Content{{
						Role: "user",
						Content: &proto.Content_Confirmation{
							Confirmation: &proto.ConfirmationContent{
								Id: conf.Id,
								Decision: &proto.ConfirmationContent_Approval{
									Approval: &proto.ApprovalDecision{Approved: true},
								},
							},
						},
					}}
				} else {
					decision = []*proto.Content{{
						Role: "user",
						Content: &proto.Content_Confirmation{
							Confirmation: &proto.ConfirmationContent{
								Id: conf.Id,
								Decision: &proto.ConfirmationContent_Decline{
									Decline: &proto.DeclineDecision{Declined: true},
								},
							},
						},
					}}
				}
				// The task is still pending, we need to only send the answer.
				// not the full history. Because we executor will put the full
				// history together.
				conf, outputs, err = runAutoExec(ctx, d, id, agentID, decision)
				if err != nil {
					return err
				}
				history = append(history, decision...)
				history = append(history, outputs...)
				if conf == nil {
					break
				}
			}
		}

		// Once we finished a task, we should start another one
		// to continue the conversation with history.
		id = uuid.NewString()
		d := internal.NewDisplay(id)
		d.DisplayHeader()

		// Remove all the function calls, confirmations,
		// and function responses. They are not relevant
		// for the upcoming executions.
		history = resetHistory(history)

		input, err = d.PromptForInput()
		if err != nil {
			return err
		}
		d.DisplayInput(input)
		history = append(history, &proto.Content{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: input,
				},
			},
		})
	}
}

func runAutoExec(ctx context.Context, d *internal.Display, id string, agentID string, inputs []*proto.Content) (*proto.ConfirmationContent, []*proto.Content, error) {
	fn := runExecHeadless
	if execServerAddr != "" {
		fn = runExecServer
	}
	return fn(ctx, d, id, agentID, inputs)
}

func runExecHeadless(ctx context.Context, d *internal.Display, id string, agentID string, inputs []*proto.Content) (*proto.ConfirmationContent, []*proto.Content, error) {
	if execController == nil {
		cfg, err := config.LoadFromFile(execConfigFile)
		if err != nil {
			return nil, nil, fmt.Errorf("error loading config file '%s': %w", execConfigFile, err)
		}

		c, err := newControllerFromConfig(ctx, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating controller: %w", err)
		}
		execController = c
	}

	var checkpoint string
	var outputs []*proto.Content
	var confirmation *proto.ConfirmationContent
	outputHandler := agent.OutputHandler(func(resp *proto.ProcessResponse) error {
		if resp.CheckpointId != "" {
			checkpoint = resp.CheckpointId
		}

		for _, c := range resp.Contents {
			if conf := c.GetConfirmation(); conf != nil {
				confirmation = conf
			}
		}
		outputs = append(outputs, resp.Contents...)
		displayContents(d, resp.Contents)
		return nil
	})
	if err := execController.Exec(ctx, id, agentID, &proto.ProcessRequest{
		Contents: inputs,
	}, outputHandler); err != nil {
		return nil, nil, fmt.Errorf("error executing with local server: %w", err)
	}

	if confirmation == nil {
		d.FinishOutput(checkpoint)
	}
	return confirmation, outputs, nil
}

func runExecServer(ctx context.Context, d *internal.Display, id string, agentID string, inputs []*proto.Content) (*proto.ConfirmationContent, []*proto.Content, error) {
	conn, err := connect(execServerAddr)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()

	client := proto.NewGARServiceClient(conn)
	stream, err := client.Exec(ctx, &proto.ExecRequest{
		Id:      id,
		AgentId: agentID,
		Inputs:    inputs,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("error executing: %w", err)
	}

	var checkpoint string
	var outputs []*proto.Content
	var confirmation *proto.ConfirmationContent
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("error receiving response: %w", err)
		}
		if resp.Outputs != nil {
			for _, c := range resp.Outputs {
				if conf := c.GetConfirmation(); conf != nil {
					confirmation = conf
				}
			}
			outputs = append(outputs, resp.Outputs...)
			displayContents(d, resp.Outputs)
		}
	}
	if confirmation == nil {
		d.FinishOutput(checkpoint)
	}
	return confirmation, outputs, nil
}

func displayContents(d *internal.Display, contents []*proto.Content) {
	for _, output := range contents {
		switch o := output.Content.(type) {
		case *proto.Content_Text:
			d.DisplayOutput(o.Text.Text)
		case *proto.Content_Confirmation:
			// Let the confirmation prompt handle displaying the question.
		case *proto.Content_FunctionCall:
			// No-op for cleaner CLI logs
		case *proto.Content_FunctionResponse:
			// Only print if the tool returned an error, otherwise skip
			respMap := o.FunctionResponse.Response.AsMap()
			if errStr, ok := respMap["error"]; ok {
				d.DisplayOutput(fmt.Sprintf("\n[TOOL ERROR for %s]\n%v\n", o.FunctionResponse.Name, errStr))
			}
		default:
			d.DisplayOutput(fmt.Sprintf("unknown output type: %v", o))
		}
	}
}

func resetHistory(history []*proto.Content) []*proto.Content {
	var out []*proto.Content
	for _, c := range history {
		if c.GetFunctionCall() != nil {
			continue
		}
		if c.GetFunctionResponse() != nil {
			continue
		}
		if c.GetConfirmation() != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}
