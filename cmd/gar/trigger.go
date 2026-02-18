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
	triggerHeadless   bool
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
	triggerCmd.Flags().StringVar(&triggerInput, "input", "", "Input message to send (required)")
	triggerCmd.Flags().StringVar(&triggerServerAddr, "server", "localhost:8494", "gRPC controller server address (default: localhost:8494)")
	triggerCmd.Flags().BoolVar(&triggerHeadless, "headless", false, "Run in headless mode with a built-in Controller")
	triggerCmd.Flags().StringVar(&triggerConfigFile, "config", "gar.yaml", "Path to YAML configuration file (only used in headless mode)")
	triggerCmd.MarkFlagRequired("input")
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
		cancel()
	}()

	return triggerLoop(ctx, triggerSessionID, triggerInput)
}

func triggerLoop(ctx context.Context, sessionID string, input string) error {
	d := internal.NewDisplay(sessionID)
	d.DisplayHeader()

	for {
		d.DisplayInput(input)

		inputs := []*proto.Content{
			{
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: input,
					},
				},
			},
		}
		if triggerHeadless {
			if err := runTriggerHeadless(ctx, d, triggerSessionID, inputs); err != nil {
				return err
			}
		} else {
			if err := runTriggerServer(ctx, d, triggerSessionID, inputs); err != nil {
				return err
			}
		}

		var err error
		input, err = d.PromptForInput()
		if err != nil {
			return err
		}
	}
}

func runTriggerHeadless(ctx context.Context, d *internal.Display, sessionID string, inputs []*proto.Content) error {
	if triggerController == nil {
		cfg, err := config.LoadFromFile(triggerConfigFile)
		if err != nil {
			return fmt.Errorf("error loading config file '%s': %w", triggerConfigFile, err)
		}

		approval := func(question string) bool {
			ok, err := d.PromptForApproval(question)
			if err != nil {
				return false
			}
			return ok
		}

		c, err := newControllerFromConfig(ctx, approval, cfg)
		if err != nil {
			return fmt.Errorf("error creating controller: %w", err)
		}
		triggerController = c
	}

	var checkpoint string
	outputHandler := agent.OutputHandler(func(resp *proto.ProcessResponse) error {
		if resp.CheckpointId != "" {
			checkpoint = resp.CheckpointId
		}

		displayContents(d, resp.Contents)
		return nil
	})
	if err := triggerController.TriggerSession(ctx, sessionID, &proto.ProcessRequest{
		Contents: inputs,
	}, outputHandler); err != nil {
		return fmt.Errorf("error triggering session in headless mode: %w", err)
	}

	d.FinishOutput(checkpoint)
	return nil
}

func runTriggerServer(ctx context.Context, d *internal.Display, sessionID string, inputs []*proto.Content) error {
	conn, err := connect(triggerServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := proto.NewGARServiceClient(conn)
	stream, err := client.TriggerSession(ctx, &proto.TriggerSessionRequest{
		SessionId: sessionID,
		Role:      "user",
		Inputs:    inputs,
	})
	if err != nil {
		return fmt.Errorf("error triggering session: %w", err)
	}

	var checkpoint string
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error receiving response: %w", err)
		}
		if resp.Outputs != nil {
			displayContents(d, resp.Outputs)
		}
		if resp.CheckpointId != "" {
			checkpoint = resp.CheckpointId
		}
	}
	d.FinishOutput(checkpoint)
	return nil
}

func displayContents(d *internal.Display, contents []*proto.Content) {
	for _, output := range contents {
		switch o := output.Content.(type) {
		case *proto.Content_Text:
			d.DisplayOutput(o.Text.Text)
		default:
			d.DisplayOutput(fmt.Sprintf("unknown output type: %v", o))
		}
	}
}
