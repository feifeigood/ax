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

// Package config provides configuration structures for AX server.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/google/ax/agent"
	"gopkg.in/yaml.v3"
)

// Config represents the main configuration for AX server.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	EventLog    EventLogConfig    `yaml:"eventlog"`
	HealthCheck HealthCheckConfig `yaml:"health_check"`
	Planner     PlannerConfig     `yaml:"planner,omitempty"`
	Registry    RegistryConfig    `yaml:"registry,omitempty"`
}

// RegistryConfig allows registring agents.
type RegistryConfig struct {
	RemoteAgents            []RemoteAgentConfig  `yaml:"remote_agents,omitempty"`
	KubernetesSandboxAgents []SandboxAgentConfig `yaml:"k8s_sandbox_agents,omitempty"`
}

// HealthCheckConfig defines the configuration for agent health checks.
// When enabled, the controller will perform active polling to check the health of registered agents and
// flip the health status of agents that are not responsive. When disabled, the controller will not perform
// any health checks and will assume all agents are healthy.
// TODO(lhuan): Add passive health checks and discovery rules.
type HealthCheckConfig struct {
	Enabled  bool          `yaml:"enabled"`  // default: false (no active polling)
	Interval time.Duration `yaml:"interval"` // default: 30s
}

// ServerConfig configures the gRPC server.
type ServerConfig struct {
	Address string `yaml:"address"` // Server address to listen on (e.g., ":8494")
}

// EventLogConfig configures the event log storage.
type EventLogConfig struct {
	SQLiteFilename string `yaml:"db_filename"` // SQLite file for event log storage
}

// PlannerConfig configures the planner.
type PlannerConfig struct {
	Gemini GeminiPlannerConfig `yaml:"gemini"`
}

// GeminiPlannerConfig configures the Gemini-based planner.
// Note: API key is not configurable here for security reasons.
// Set GEMINI_API_KEY environment variable instead.
type GeminiPlannerConfig struct {
	Model         string        `yaml:"model,omitempty"` // Model name
	Temperature   float32       `yaml:"temperature,omitempty"`
	MaxTokens     int32         `yaml:"max_tokens,omitempty"`
	Timeout       time.Duration `yaml:"timeout,omitempty"`
	ContextWindow int           `yaml:"context_window,omitempty"`
	SystemPrompt  string        `yaml:"system_prompt,omitempty"`
	SkillsDir     string        `yaml:"skills_dir,omitempty"` // Directory to discover skills from
}

// RemoteAgentConfig configures a remote agent to register on startup.
type RemoteAgentConfig struct {
	ID          string            `yaml:"id"`                 // Unique agent identifier
	Name        string            `yaml:"name"`               // Human-readable name
	Description string            `yaml:"description"`        // Description of agent capabilities
	Address     string            `yaml:"address"`            // gRPC address (e.g., "localhost:50051")
	Metadata    map[string]string `yaml:"metadata,omitempty"` // Optional metadata
}

// SandboxAgentConfig configures a Kubernetes Sandbox agent to register on startup.
type SandboxAgentConfig struct {
	ID                 string `yaml:"id"`                   // Unique agent identifier
	Name               string `yaml:"name"`                 // Human-readable name
	Description        string `yaml:"description"`          // Description of agent capabilities
	SandboxTemplateRef string `yaml:"sandbox_template_ref"` // Name of the SandboxTemplate CR to use
	// TODO: implement shutdown_time for automatic sandbox shutdown
	ContainerPort int               `yaml:"container_port,omitempty"` // Optional container port, defaults to 8494
	UseRouter     bool              `yaml:"use_router,omitempty"`     // Override port-forwarding to use Sandbox Router
	Metadata      map[string]string `yaml:"metadata,omitempty"`       // Optional metadata
}

type LocalAgentConfig struct {
	ID          string            `yaml:"id"`                 // Unique agent identifier
	Name        string            `yaml:"name"`               // Human-readable name
	Description string            `yaml:"description"`        // Description of agent capabilities
	Metadata    map[string]string `yaml:"metadata,omitempty"` // Optional metadata
	Agent       agent.Agent       // In-process agent instance (not in YAML)
}

// LoadFromFile loads configuration from a YAML file.
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set defaults
	cfg.setDefaults()

	return &cfg, nil
}

// setDefaults sets default values for optional fields.
func (c *Config) setDefaults() {
	// Server defaults
	if c.Server.Address == "" {
		c.Server.Address = ":8494"
	}

	// EventLog defaults
	if c.EventLog.SQLiteFilename == "" {
		c.EventLog.SQLiteFilename = "eventlog/log.sqlite"
	}

	// HealthCheck defaults
	if c.HealthCheck.Enabled && c.HealthCheck.Interval == 0 {
		c.HealthCheck.Interval = 30 * time.Second
	}
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.Server.Address == "" {
		return fmt.Errorf("server.address is required")
	}
	if c.EventLog.SQLiteFilename == "" {
		return fmt.Errorf("eventlog.db_filename is required")
	}
	// Validate health check
	if c.HealthCheck.Enabled {
		if c.HealthCheck.Interval <= 0 {
			return fmt.Errorf("health_check.interval must be positive when enabled")
		}
	}

	return nil
}
