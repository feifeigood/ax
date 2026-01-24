// Package config provides configuration structures for GAR server.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the main configuration for GAR server.
type Config struct {
	Server              ServerConfig        `yaml:"server"`
	EventLog            EventLogConfig      `yaml:"eventlog"`
	MaxSteps            int                 `yaml:"max_steps"`             // Maximum steps per trigger
	HealthCheckInterval time.Duration       `yaml:"health_check_interval"` // Health check interval for agents
	GeminiPlanner       GeminiPlannerConfig `yaml:"gemini_planner,omitempty"`
	RemoteAgents        []RemoteAgentConfig `yaml:"remote_agents,omitempty"` // List of remote agents to register
}

// ServerConfig configures the gRPC server.
type ServerConfig struct {
	Address string `yaml:"address"` // Server address to listen on (e.g., ":8494")
}

// EventLogConfig configures the event log storage.
type EventLogConfig struct {
	Dir string `yaml:"dir"` // Directory for event log files
}

// ControllerConfig configures the controller behavior.
type ControllerConfig struct {
	MaxSteps            int           `yaml:"max_steps"`             // Maximum steps per trigger
	HealthCheckInterval time.Duration `yaml:"health_check_interval"` // Health check interval for agents
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
}

// RemoteAgentConfig configures a remote agent to register on startup.
type RemoteAgentConfig struct {
	ID          string            `yaml:"id"`                 // Unique agent identifier
	Name        string            `yaml:"name"`               // Human-readable name
	Description string            `yaml:"description"`        // Description of agent capabilities
	Address     string            `yaml:"address"`            // gRPC address (e.g., "localhost:50051")
	Metadata    map[string]string `yaml:"metadata,omitempty"` // Optional metadata
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
	if c.EventLog.Dir == "" {
		c.EventLog.Dir = "eventlog"
	}

	// Controller defaults
	if c.MaxSteps == 0 {
		c.MaxSteps = 100
	}
	if c.HealthCheckInterval == 0 {
		c.HealthCheckInterval = 30 * time.Second
	}

	// Gemini planner defaults are handled in the controller package
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.Server.Address == "" {
		return fmt.Errorf("server.address is required")
	}
	if c.EventLog.Dir == "" {
		return fmt.Errorf("eventlog.dir is required")
	}
	if c.MaxSteps <= 0 {
		return fmt.Errorf("max_steps must be positive")
	}
	if c.HealthCheckInterval <= 0 {
		return fmt.Errorf("health_check_interval must be positive")
	}
	return nil
}
