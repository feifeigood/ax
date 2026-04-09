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

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/config"
)

// AgentType represents the type of agent (local or remote).
type AgentType string

const (
	AgentTypeLocal             AgentType = "local"
	AgentTypeRemote            AgentType = "remote"
	AgentTypeKubernetesSandbox AgentType = "k8s_sandbox"
	AgentTypeColab             AgentType = "colab"
)

// AgentInfo contains metadata about a registered agent.
type AgentInfo struct {
	ID          string
	Name        string
	Description string
	Type        AgentType
	Metadata    map[string]string
}

// Registry manages a collection of local and remote agents.
// It provides agent discovery, health monitoring, and load balancing.
type Registry struct {
	mu        sync.RWMutex
	agents    map[string]agent.Agent
	agentInfo map[string]*AgentInfo
}

// NewRegistry creates a new agent registry.
func NewRegistry() *Registry {
	return &Registry{
		agents:    make(map[string]agent.Agent),
		agentInfo: make(map[string]*AgentInfo),
	}
}

func (r *Registry) Map() map[string]agent.Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.agents
}

// RegisterLocal registers a local (in-process) agent.
func (r *Registry) RegisterLocal(cfg config.LocalAgentConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := validateID(cfg.ID); err != nil {
		return err
	}

	if _, ok := r.agents[cfg.ID]; ok {
		return fmt.Errorf("agent %s already registered", cfg.ID)
	}

	r.agents[cfg.ID] = cfg.Agent
	r.agentInfo[cfg.ID] = &AgentInfo{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Description: cfg.Description,
		Type:        AgentTypeLocal,
		Metadata:    cfg.Metadata,
	}

	return nil
}

// RegisterRemote registers a remote agent by creating a remote agent client.
func (r *Registry) RegisterRemote(cfg config.RemoteAgentConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := validateID(cfg.ID); err != nil {
		return err
	}

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
		ID:          cfg.ID,
		Name:        cfg.Name,
		Description: cfg.Description,
		Type:        AgentTypeRemote,
		Metadata:    cfg.Metadata,
	}

	return nil
}

// RegisterKubernetesSandbox registers a sandbox agent by dynamically provisioning a Sandbox on GKE.
func (r *Registry) RegisterKubernetesSandbox(ctx context.Context, cfg config.SandboxAgentConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := validateID(cfg.ID); err != nil {
		return err
	}

	if _, ok := r.agents[cfg.ID]; ok {
		return fmt.Errorf("agent %s already registered", cfg.ID)
	}

	sandboxAgent, err := agent.NewKubernetesSandboxAgent(ctx, agent.KubernetesSandboxAgentConfig{
		ID:                 cfg.ID,
		SandboxTemplateRef: cfg.SandboxTemplateRef,
		ContainerPort:      cfg.ContainerPort,
		UseRouter:          cfg.UseRouter,
	})
	if err != nil {
		return fmt.Errorf("failed to provision sandbox agent: %w", err)
	}

	r.agents[cfg.ID] = sandboxAgent
	r.agentInfo[cfg.ID] = &AgentInfo{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Description: cfg.Description,
		Type:        AgentTypeKubernetesSandbox,
		Metadata:    cfg.Metadata,
	}

	return nil
}

// RegisterColab registers a Colab agent that executes a local Python file
// on a remote Colab session via the colab CLI.
func (r *Registry) RegisterColab(cfg config.ColabAgentConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := validateID(cfg.ID); err != nil {
		return err
	}

	if _, ok := r.agents[cfg.ID]; ok {
		return fmt.Errorf("agent %s already registered", cfg.ID)
	}

	colabAgent, err := agent.NewColabAgent(agent.ColabAgentConfig{
		ID:              cfg.ID,
		LocalFile:       cfg.LocalFile,
		DriveFile:       cfg.DriveFile,
		Accelerator:     cfg.Accelerator,
		DriveMountPath:  cfg.DriveMountPath,
		Requirements:    cfg.Requirements,
		InputFlag:       cfg.InputFlag,
		OutputImage:     cfg.OutputImage,
		OutputDrivePath: cfg.OutputDrivePath,
	})
	if err != nil {
		return fmt.Errorf("failed to create colab agent: %w", err)
	}

	r.agents[cfg.ID] = colabAgent
	r.agentInfo[cfg.ID] = &AgentInfo{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Description: cfg.Description,
		Type:        AgentTypeColab,
		Metadata:    cfg.Metadata,
	}

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

// Close stops the registry and closes all agents.
func (r *Registry) Close() error {

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
