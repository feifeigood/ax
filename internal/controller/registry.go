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

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/config"
)

// AgentType represents the type of agent (local or remote).
type AgentType string

const (
	AgentTypeLocal  AgentType = "local"
	AgentTypeRemote AgentType = "remote"
)

// AgentInfo contains metadata about a registered agent.
type AgentInfo struct {
	ID              string
	Name            string
	Description     string
	Type            AgentType
	Healthy         bool
	LastHealthCheck time.Time
	Metadata        map[string]string
}

// Registry manages a collection of local and remote agents.
// It provides agent discovery, health monitoring, and load balancing.
type Registry struct {
	mu        sync.RWMutex
	agents    map[string]agent.Agent
	agentInfo map[string]*AgentInfo
	monitor   *HealthMonitor
}

// NewRegistry creates a new agent registry.
func NewRegistry(healthCheckConfig config.HealthCheckConfig) (*Registry, error) {
	if healthCheckConfig.Enabled && healthCheckConfig.Interval <= 0 {
		return nil, fmt.Errorf("invalid health check interval: %v", healthCheckConfig.Interval)
	}

	r := &Registry{
		agents:    make(map[string]agent.Agent),
		agentInfo: make(map[string]*AgentInfo),
	}
	if healthCheckConfig.Enabled {
		r.monitor = NewHealthMonitor(healthCheckConfig, r)
		r.monitor.Start()
	}

	return r, nil
}

// RegisterLocal registers a local (in-process) agent.
func (r *Registry) RegisterLocal(cfg config.LocalAgentConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[cfg.ID]; ok {
		return fmt.Errorf("agent %s already registered", cfg.ID)
	}

	r.agents[cfg.ID] = cfg.Agent
	r.agentInfo[cfg.ID] = &AgentInfo{
		ID:              cfg.ID,
		Name:            cfg.Name,
		Description:     cfg.Description,
		Type:            AgentTypeLocal,
		Healthy:         true,
		LastHealthCheck: time.Now(),
		Metadata:        cfg.Metadata,
	}

	return nil
}

// RegisterRemote registers a remote agent by creating a remote agent client.
func (r *Registry) RegisterRemote(cfg config.RemoteAgentConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// TODO(lhuan): Consider enforcing health check during registration. Only allow registration if the agent is reachable and healthy.

	if _, ok := r.agents[cfg.ID]; ok {
		return fmt.Errorf("agent %s already registered", cfg.ID)
	}

	// Create remote agent client
	remoteAgent, err := agent.NewRemoteAgent(agent.RemoteAgentConfig{
		Address:    cfg.Address,
		Reconnect:  true,
		MaxRetries: 3,
	})
	if err != nil {
		return fmt.Errorf("failed to create remote agent: %w", err)
	}

	r.agents[cfg.ID] = remoteAgent
	r.agentInfo[cfg.ID] = &AgentInfo{
		ID:              cfg.ID,
		Name:            cfg.Name,
		Description:     cfg.Description,
		Type:            AgentTypeRemote,
		Healthy:         (r.monitor == nil), // Optimistic if no health check, pessimistic if health check is enabled
		LastHealthCheck: time.Time{},
		Metadata:        cfg.Metadata,
	}

	return nil
}

// Unregister removes an agent from the registry.
func (r *Registry) Unregister(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[id]
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}

	// Close the agent
	if err := agent.Close(); err != nil {
		return fmt.Errorf("failed to close agent: %w", err)
	}

	delete(r.agents, id)
	delete(r.agentInfo, id)

	return nil
}

// Get retrieves an agent by ID.
func (r *Registry) Get(id string) (agent.Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	a, exists := r.agents[id]
	if !exists {
		return nil, fmt.Errorf("agent %s not found", id)
	}

	return a, nil
}

// GetInfo retrieves agent metadata by ID.
func (r *Registry) GetInfo(id string) (*AgentInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.agentInfo[id]
	if !ok {
		return nil, fmt.Errorf("agent %s not found", id)
	}

	return info, nil
}

// List returns all registered agent IDs.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}

	return ids
}

// ListHealthy returns all healthy agent IDs.
func (r *Registry) ListHealthy() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0)
	for id, info := range r.agentInfo {
		if info.Healthy {
			ids = append(ids, id)
		}
	}

	return ids
}


// healthCheck performs a health check on a specific agent.
func (r *Registry) healthCheck(id string) error {
	a, err := r.Get(id)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = a.HealthCheck(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.agentInfo[id]
	if exists {
		info.Healthy = (err == nil)
		info.LastHealthCheck = time.Now()
	}

	return err
}


// Close stops the registry and closes all agents.
func (r *Registry) Close() error {
	// Stop health checks before acquiring the lock to avoid deadlock
	// (monitor.Stop waits for background routines that might need the lock)
	if r.monitor != nil {
		r.monitor.Stop()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Close all agents
	var firstErr error
	for id, a := range r.agents {
		if err := a.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to close agent %s: %w", id, err)
		}
	}

	return firstErr
}
