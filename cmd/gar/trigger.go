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
	triggerSessionID  string
	triggerInput      string
	triggerServerAddr string
	triggerConfigFile string
)

var triggerCmd = &cobra.Command{
	Use:   "trigger",
	Short: "Trigger a new session or resume an existing one",
	Long: `Trigger a new agentic session or resume an existing one.
If no session ID is provided, a new UUID will be generated.`,
	RunE: runTrigger,
}

func init() {
	triggerCmd.Flags().StringVar(&triggerSessionID, "session", "", "Session ID (optional, generates UUID if not provided)")
	triggerCmd.Flags().StringVar(&triggerInput, "input", "", "Input message to send (optional)")
	triggerCmd.Flags().StringVar(&triggerServerAddr, "server", "", "gRPC controller server address (if specified, connects to remote server; otherwise runs with a local built-in GAR server)")
	triggerCmd.Flags().StringVar(&triggerConfigFile, "config", "gar.yaml", "Path to YAML configuration file (only used with a local built-in GAR server)")
}

// TODO(jbd): Add multimodal input flags, e.g. --input-image.

var (
	triggerController *controller.Controller
)

func runTrigger(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Generate UUID if no session ID provided
	if triggerSessionID == "" {
		triggerSessionID = uuid.New().String()
	}

	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nReceived interrupt, shutting down...")
		if triggerController != nil {
			triggerController.Close()
		}
		cancel()
	}()

	return triggerLoop(ctx, triggerSessionID, triggerInput)
}

func triggerLoop(ctx context.Context, sessionID string, input string) error {
	d := internal.NewDisplay(sessionID)
	d.DisplayHeader()

	if input == "" {
		var err error
		input, err = d.PromptForInput()
		if err != nil {
			return err
		}
	}

	d.DisplayInput(input)
	inputs := []*proto.Content{
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
		var conf *proto.ConfirmationContent
		var err error
		if triggerServerAddr == "" {
			conf, err = runTriggerHeadless(ctx, d, triggerSessionID, inputs)
		} else {
			conf, err = runTriggerServer(ctx, d, triggerSessionID, inputs)
		}
		if err != nil {
			return err
		}

		if conf != nil {
			approved, err := d.PromptForApproval(conf.Question)
			if err != nil {
				return err
			}
			if approved {
				inputs = []*proto.Content{{
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
				inputs = []*proto.Content{{
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
			continue
		}

		input, err = d.PromptForInput()
		if err != nil {
			return err
		}
		d.DisplayInput(input)
		inputs = []*proto.Content{
			{
				Role: "user",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: input,
					},
				},
			},
		}
	}
}

func runTriggerHeadless(ctx context.Context, d *internal.Display, sessionID string, inputs []*proto.Content) (*proto.ConfirmationContent, error) {
	if triggerController == nil {
		cfg, err := config.LoadFromFile(triggerConfigFile)
		if err != nil {
			return nil, fmt.Errorf("error loading config file '%s': %w", triggerConfigFile, err)
		}

		c, err := newControllerFromConfig(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("error creating controller: %w", err)
		}
		triggerController = c
	}

	var checkpoint string
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

		displayContents(d, resp.Contents)
		return nil
	})
	if err := triggerController.TriggerSession(ctx, sessionID, &proto.ProcessRequest{
		Contents: inputs,
	}, outputHandler); err != nil {
		return nil, fmt.Errorf("error triggering session with local server: %w", err)
	}

	if confirmation == nil {
		d.FinishOutput(checkpoint)
	}
	return confirmation, nil
}

func runTriggerServer(ctx context.Context, d *internal.Display, sessionID string, inputs []*proto.Content) (*proto.ConfirmationContent, error) {
	conn, err := connect(triggerServerAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := proto.NewGARServiceClient(conn)
	stream, err := client.TriggerSession(ctx, &proto.TriggerSessionRequest{
		SessionId: sessionID,
		Inputs:    inputs,
	})
	if err != nil {
		return nil, fmt.Errorf("error triggering session: %w", err)
	}

	var checkpoint string
	var confirmation *proto.ConfirmationContent
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error receiving response: %w", err)
		}
		if resp.Outputs != nil {
			for _, c := range resp.Outputs {
				if conf := c.GetConfirmation(); conf != nil {
					confirmation = conf
				}
			}
			displayContents(d, resp.Outputs)
		}
		if resp.CheckpointId != "" {
			checkpoint = resp.CheckpointId
		}
	}
	if confirmation == nil {
		d.FinishOutput(checkpoint)
	}
	return confirmation, nil
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
