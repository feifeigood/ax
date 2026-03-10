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

	"github.com/google/gar/internal/sandboxclient"
	"github.com/google/gar/proto"
	"google.golang.org/grpc/metadata"
)

type SandboxAgent struct {
	client    *sandboxclient.Client
	config    SandboxAgentConfig
	inCluster bool
	poolName  string

	claimMu      sync.Mutex
	activeClaims map[string]struct{}
}

// SandboxAgentConfig configures a sandbox agent client.
type SandboxAgentConfig struct {
	ID                 string
	SandboxTemplateRef string

	ContainerPort      int
	WarmPoolSize       int
	UseRouter          bool
	RouterAddress      string
}

// NewSandboxAgent creates a new sandbox agent logic coordinator.
func NewSandboxAgent(ctx context.Context, config SandboxAgentConfig) (*SandboxAgent, error) {
	namespace := "default"
	//TODO(lhuan): allow setup in yaml
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		namespace = ns
	}

	client, err := sandboxclient.NewClient(namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox client: %w", err)
	}

	return newSandboxAgentWithClient(ctx, client, config)
}

// newSandboxAgentWithClient allows customizing the client instantiation.
func newSandboxAgentWithClient(ctx context.Context, client *sandboxclient.Client, config SandboxAgentConfig) (*SandboxAgent, error) {
	agent := &SandboxAgent{
		client:       client,
		config:       config,
		inCluster:    os.Getenv("KUBERNETES_SERVICE_HOST") != "",
		activeClaims: make(map[string]struct{}),
	}

	// Create a SandboxWarmPool if configured
	if config.WarmPoolSize > 0 {
		poolName := fmt.Sprintf("gar-pool-%s", config.ID)
		agent.poolName = poolName

		// TODO: This forced recreation can cause a race condition if multiple 
    // GAR controller instances are running in the same namespace with the 
    // same agent ID. Consider using a 'Get or Create' pattern with 
    // version/generation checks for multi-instance deployments.
		_ = client.DeleteWarmPool(ctx, poolName)
		if err := client.CreateWarmPool(ctx, poolName, config.SandboxTemplateRef, config.WarmPoolSize); err != nil {
			return nil, fmt.Errorf("failed to create SandboxWarmPool: %w", err)
		}
	}

	return agent, nil
}

// HealthCheck always returns nil for ephemeral agents since health is checked during Process.
func (a *SandboxAgent) HealthCheck(ctx context.Context) error {
// TODO: implement proper health check logic for sandbox agents.
	return nil
}

func (a *SandboxAgent) Process(ctx context.Context, sessionID string, req *proto.ProcessRequest, handler OutputHandler) error {
	// Use the sessionID deterministically so interactive sessions reuse the same sandbox pod,
	// rather than spawning a new pod for every exchanged message.
	safeSessionID := strings.ReplaceAll(sessionID, "-", "")
	if len(safeSessionID) > 20 {
		safeSessionID = safeSessionID[:20] // Keep it short for k8s names
	}
	claimName := fmt.Sprintf("gar-claim-%s-%s", a.config.ID, safeSessionID)

	// 1. Create Ephemeral SandboxClaim (Ignore AlreadyExists errors if it's already created this session)
	if err := a.client.CreateClaim(ctx, claimName, a.config.SandboxTemplateRef); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("failed to create SandboxClaim: %w", err)
		}
	}

	// We track the claim so `Close()` deletes it at the end of the session.
	// If we deleted it here, interactive shells would lose the pod.
	a.claimMu.Lock()
	a.activeClaims[claimName] = struct{}{}
	a.claimMu.Unlock()

	// Wait for Sandbox to bound and get the warm pool pod selector
	selector, err := a.client.WaitForSandbox(ctx, claimName)
	if err != nil {
		return err
	}

	// Wait for the Sandbox to become Ready so its DNS record is populated
	if err := a.client.WaitForSandboxReady(ctx, claimName); err != nil {
		log.Printf("Warning: failed to wait for sandbox readiness: %v", err)
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
	remoteAgent, err := NewRemoteAgent(RemoteAgentConfig{
		Address:    remoteAddr,
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
	return remoteAgent.Process(ctx, sessionID, req, handler)
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

// Close gracefully deletes the SandboxWarmPool if one was created,
// and shreds all active SandboxClaims generated by this agent during interactive sessions.
func (a *SandboxAgent) Close() error {
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

	if a.poolName != "" {
		if err := a.client.DeleteWarmPool(ctx, a.poolName); err != nil {
			errs = append(errs, fmt.Sprintf("failed to delete warm pool %q: %v", a.poolName, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}
