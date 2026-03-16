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
	"log"
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/google/ax/agent"
	"github.com/google/ax/internal/config"
	"github.com/google/ax/internal/controller"
	"github.com/google/ax/internal/controller/task"
	"github.com/google/ax/internal/server"
	"github.com/spf13/cobra"
)

var (
	serveConfigFile string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run controller as a gRPC server",
	Long: `Run the AX controller as a gRPC server.
Loads configuration from a YAML file (default: ax.yaml).`,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveConfigFile, "config", "ax.yaml", "Path to YAML configuration file")
}

func runServe(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Load configuration from YAML file
	cfg, err := config.LoadFromFile(serveConfigFile)
	if err != nil {
		return fmt.Errorf("error loading config file '%s': %w\nTip: Create a config file with 'ax serve --help' to see an example", serveConfigFile, err)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	c, err := newControllerFromConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}
	defer c.Close()

	// Create server
	srv := server.New(c)

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("\nReceived interrupt, shutting down...")
		srv.GracefulStop()
	}()

	log.Printf("Starting AX server at %s...\n", cfg.Server.Address)
	if err := srv.Serve(cfg.Server.Address); err != nil {
		return fmt.Errorf("error serving: %w", err)
	}

	return nil
}

func newControllerFromConfig(ctx context.Context, cfg *config.Config) (*controller.Controller, error) {
	// Create event log builder
	eventLogBuilder := func(taskID string) (task.EventLog, error) {
		path := path.Join(cfg.EventLog.Dir, taskID+".jsonl")
		return task.OpenFileEventLog(path)
	}

	// Create planner builder
	plannerBuilder := func(ctx context.Context, r *controller.Registry) (agent.Agent, error) {
		// The builder defines which planner to use.
		// Currently, it uses the Gemini planner.
		// Gemini config can be customized via environment variables (GEMINI_API_KEY, AX_GEMINI_MODEL)
		// TODO(lhuan): allow other planners based on cfg.PlannerType
		return controller.NewGeminiPlannerAgent(ctx, r, controller.GeminiPlannerConfig{
			GeminiConfig: controller.GeminiConfig{
				Model:        cfg.Planner.Gemini.Model,
				MaxTokens:    cfg.Planner.Gemini.MaxTokens,
				Timeout:      cfg.Planner.Gemini.Timeout,
				SystemPrompt: cfg.Planner.Gemini.SystemPrompt,
			},
			SkillsDir: cfg.Planner.Gemini.SkillsDir,
		})
	}

	// Build controller config
	controllerConfig := controller.Config{
		EventLogBuilder: eventLogBuilder,
		PlannerBuilder:  plannerBuilder,
		HealthCheck:     cfg.HealthCheck,
	}

	// Create controller
	c, err := controller.New(ctx, controllerConfig)
	if err != nil {
		return nil, err
	}

	for _, agentCfg := range cfg.Registry.RemoteAgents {
		if err := c.Registry().RegisterRemote(agentCfg); err != nil {
			return nil, fmt.Errorf("failed to register remote agent %s: %w", agentCfg.ID, err)
		}
	}

	for _, agentCfg := range cfg.Registry.KubernetesSandboxAgents {
		if err := c.Registry().RegisterKubernetesSandbox(ctx, agentCfg); err != nil {
			return nil, fmt.Errorf("failed to register kubernetes sandbox agent %s: %w", agentCfg.ID, err)
		}
	}
	return c, nil
}
