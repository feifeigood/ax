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
	Server        ServerConfig        `yaml:"server"`
	EventLog      EventLogConfig      `yaml:"eventlog"`
	Controller    ControllerConfig    `yaml:"controller"`
	GeminiPlanner GeminiPlannerConfig `yaml:"gemini_planner,omitempty"`
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
	MaxSteps            int           `yaml:"max_steps"`             // Maximum steps per session
	HealthCheckInterval time.Duration `yaml:"health_check_interval"` // Health check interval for agents
}

// GeminiPlannerConfig configures the Gemini-based planner.
// Note: API key is not configurable here for security reasons.
// Set GEMINI_API_KEY environment variable instead.
type GeminiPlannerConfig struct {
	Model         string        `yaml:"model,omitempty"`    // Model name
	Temperature   float32       `yaml:"temperature,omitempty"`
	MaxTokens     int32         `yaml:"max_tokens,omitempty"`
	Timeout       time.Duration `yaml:"timeout,omitempty"`
	ContextWindow int           `yaml:"context_window,omitempty"`
	SystemPrompt  string        `yaml:"system_prompt,omitempty"`
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
	if c.Controller.MaxSteps == 0 {
		c.Controller.MaxSteps = 100
	}
	if c.Controller.HealthCheckInterval == 0 {
		c.Controller.HealthCheckInterval = 30 * time.Second
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
	if c.Controller.MaxSteps <= 0 {
		return fmt.Errorf("controller.max_steps must be positive")
	}
	if c.Controller.HealthCheckInterval <= 0 {
		return fmt.Errorf("controller.health_check_interval must be positive")
	}
	return nil
}
