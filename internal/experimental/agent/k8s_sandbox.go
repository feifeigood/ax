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

package agent

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/experimental/k8s/sandboxclient"
	"github.com/google/ax/proto"
	"google.golang.org/grpc/metadata"
)

type KubernetesSandboxAgent struct {
	client    *sandboxclient.Client
	config    KubernetesSandboxAgentConfig
	inCluster bool

	claimMu      sync.Mutex
	activeClaims map[string]struct{}
}

// KubernetesSandboxAgentConfig configures a sandbox agent client.
type KubernetesSandboxAgentConfig struct {
	ID                 string
	SandboxTemplateRef string

	ContainerPort int
	UseRouter     bool
	RouterAddress string
}

// NewKubernetesSandboxAgent creates a new sandbox agent logic coordinator.
func NewKubernetesSandboxAgent(ctx context.Context, config KubernetesSandboxAgentConfig) (*KubernetesSandboxAgent, error) {
	namespace := "default"
	//TODO(lhuan): allow setup in yaml
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		namespace = ns
	}

	client, err := sandboxclient.NewClient(namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox client: %w", err)
	}

	return newKubernetesSandboxAgentWithClient(ctx, client, config)
}

// newKubernetesSandboxAgentWithClient allows customizing the client instantiation.
func newKubernetesSandboxAgentWithClient(ctx context.Context, client *sandboxclient.Client, config KubernetesSandboxAgentConfig) (*KubernetesSandboxAgent, error) {
	agent := &KubernetesSandboxAgent{
		client:       client,
		config:       config,
		inCluster:    os.Getenv("KUBERNETES_SERVICE_HOST") != "",
		activeClaims: make(map[string]struct{}),
	}

	return agent, nil
}

func (a *KubernetesSandboxAgent) Connect(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Use the id deterministically so interactive executions reuse the same sandbox pod,
	// rather than spawning a new pod for every exchanged message.
	safeID := strings.ReplaceAll(execID, "-", "")
	if len(safeID) > 20 {
		safeID = safeID[:20] // Keep it short for k8s names
	}
	claimName := fmt.Sprintf("ax-claim-%s-%s", a.config.ID, safeID)

	// 1. Create Ephemeral SandboxClaim (Ignore AlreadyExists errors if it's already created this execution)
	if err := a.client.CreateClaim(ctx, claimName, a.config.SandboxTemplateRef); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("failed to create SandboxClaim: %w", err)
		}
	}

	// We track the claim so `Close()` deletes it at the end of the execution.
	// If we deleted it here, interactive shells would lose the pod.
	a.claimMu.Lock()
	a.activeClaims[claimName] = struct{}{}
	a.claimMu.Unlock()

	// Wait for Sandbox to bound and get the warm pool pod selector
	selector, err := a.client.WaitForSandbox(ctx, claimName)
	if err != nil {
		return fmt.Errorf("failed to wait for sandbox: %w", err)
	}

	// 2. Setup Connection (Direct or Router)
	targetPort := a.config.ContainerPort
	if targetPort == 0 {
		targetPort = 8494
	}

	var remoteAddr string
	var pfCancel func()
	var errPF error

	namespace := "default"
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		namespace = ns
	}

	if a.config.UseRouter {
		// Use Sandbox Router
		routerAddr := a.config.RouterAddress
		if routerAddr == "" {
			routerAddr = fmt.Sprintf("sandbox-router-svc.%s.svc.cluster.local:8080", namespace)
		}

		if !a.inCluster {
			// Local testing: port-forward the router service
			localPort, err := findAvailablePort()
			if err != nil {
				return err
			}
			pfCancel, errPF = a.client.PortForwardRouter(ctx, localPort, 8080)
			if errPF != nil {
				return fmt.Errorf("router port-forward failed: %w", errPF)
			}
			remoteAddr = fmt.Sprintf("127.0.0.1:%d", localPort)
		} else {
			remoteAddr = routerAddr
		}

		// Add routing headers
		ctx = metadata.AppendToOutgoingContext(ctx,
			"x-sandbox-id", claimName,
			"x-sandbox-namespace", namespace,
			"x-sandbox-port", fmt.Sprintf("%d", targetPort),
		)
	} else {
		// Direct to Pod
		if !a.inCluster {
			localPort, err := findAvailablePort()
			if err != nil {
				return err
			}
			pfCancel, errPF = a.client.PortForward(ctx, claimName, selector, localPort, targetPort)
			if errPF != nil {
				return fmt.Errorf("port-forward failed: %w", errPF)
			}
			remoteAddr = fmt.Sprintf("127.0.0.1:%d", localPort)
			log.Printf("Sandbox port-forward established at: %s", remoteAddr)
		} else {
			// This case is rare for direct-to-pod in cluster (usually uses service)
			// But for now, we'll assume the service is named after the claim
			remoteAddr = fmt.Sprintf("%s.%s.svc.cluster.local:%d", claimName, namespace, targetPort)
		}
	}

	if pfCancel != nil {
		defer pfCancel()
	}

	// Connect to the agent (either direct or via router)
	remoteAgent, err := agent.NewRemoteAgent(agent.RemoteAgentConfig{
		Address: remoteAddr,
		// TODO: allow setup reconnect and max retires in yaml.
		// TODO: implement dynamic mTLS or PSK interceptors to secure the unauthenticated gRPC Sandbox transport
		Reconnect:  true,
		MaxRetries: 3,
	})
	if err != nil {
		return fmt.Errorf("failed to create remote agent connection: %w", err)
	}
	defer remoteAgent.Close()

	// Forward the request!
	return remoteAgent.Connect(ctx, conversationID, execID, start, e, o)
}

func findAvailablePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to resolve local address: %w", err)
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("failed to bind to local port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Close gracefully shreds all active SandboxClaims generated by this agent
// during interactive executions.
func (a *KubernetesSandboxAgent) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var errs []string

	a.claimMu.Lock()
	for claimName := range a.activeClaims {
		if err := a.client.DeleteClaim(ctx, claimName); err != nil {
			errs = append(errs, fmt.Sprintf("failed to delete claim %q: %v", claimName, err))
		}
	}
	a.claimMu.Unlock()

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}
