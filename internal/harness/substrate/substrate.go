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

package substrate

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/google/ax/internal/harness"
	"github.com/google/ax/internal/k8s/ate"
	"github.com/google/ax/proto"
	"github.com/google/uuid"
)

// Compile-time interface assertions.
var _ harness.Harness = (*SubstrateHarness)(nil)
var _ harness.Execution = (*substrateExecution)(nil)

// healthCheckTimeout defines the maximum time Start waits for a freshly
// created/resumed actor's harness to become reachable and ready.
const healthCheckTimeout = 60 * time.Second

const defaultWarmIdleTimeout = 30 * time.Second

type idleMode uint8

const (
	idleModeImmediateSuspend idleMode = iota
	idleModeWarmThenSuspend
)

type warmActorState struct {
	generation uint64
	workerAddr string
	inTurn     bool
	timer      *time.Timer
	suspending chan struct{}
}

// SubstrateHarness manages execution in a SubstrATE sandboxed actor over gRPC HarnessService.
type SubstrateHarness struct {
	harnessID string
	ateClient *ate.Client
	port      int
	dialOpts  []grpc.DialOption

	idleMode    idleMode
	idleTimeout time.Duration
	idleMu      sync.Mutex
	warmActors  map[string]*warmActorState
}

// New creates a new SubstrateHarness.
func New(harnessID string, endpoint string, namespace string, template string, port int, opts ...grpc.DialOption) (*SubstrateHarness, error) {
	idleMode, idleTimeout, err := idlePolicyFromEnv()
	if err != nil {
		return nil, err
	}
	if port == 0 {
		port = 50053 // Default HarnessService port
	}
	if namespace == "" {
		namespace = "ax"
	}
	if template == "" {
		template = "ax-harness-antigravity-template"
	}
	controlCreds := grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}))
	client, err := ate.NewClient(namespace, template, endpoint, controlCreds)
	if err != nil {
		return nil, fmt.Errorf("failed to create ATE client: %w", err)
	}
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	return &SubstrateHarness{
		harnessID:   harnessID,
		ateClient:   client,
		port:        port,
		dialOpts:    opts,
		idleMode:    idleMode,
		idleTimeout: idleTimeout,
	}, nil
}

func idlePolicyFromEnv() (idleMode, time.Duration, error) {
	modeValue := os.Getenv("AX_SUBSTRATE_IDLE_MODE")
	switch modeValue {
	case "", "immediate-suspend":
		return idleModeImmediateSuspend, 0, nil
	case "warm-then-suspend":
		timeoutValue := os.Getenv("AX_SUBSTRATE_IDLE_TIMEOUT")
		if timeoutValue == "" {
			return idleModeWarmThenSuspend, defaultWarmIdleTimeout, nil
		}
		timeout, err := time.ParseDuration(timeoutValue)
		if err != nil {
			return idleModeImmediateSuspend, 0, fmt.Errorf("invalid AX_SUBSTRATE_IDLE_TIMEOUT %q: %w", timeoutValue, err)
		}
		if timeout <= 0 {
			return idleModeImmediateSuspend, 0, fmt.Errorf("AX_SUBSTRATE_IDLE_TIMEOUT must be positive")
		}
		return idleModeWarmThenSuspend, timeout, nil
	default:
		return idleModeImmediateSuspend, 0, fmt.Errorf("invalid AX_SUBSTRATE_IDLE_MODE %q", modeValue)
	}
}

// Start implements Harness interface. It creates/resumes the target actor.
func (h *SubstrateHarness) Start(ctx context.Context, conversationID string, harnessConfig []byte) (execution harness.Execution, err error) {
	if conversationID == "" {
		return nil, errors.New("SubstrateHarness needs valid conversationID")
	}

	workerAddr, reusedWarmActor, err := h.beginWarmTurn(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if h.idleMode == idleModeWarmThenSuspend {
		defer func() {
			if err != nil {
				h.abortWarmTurn(conversationID)
			}
		}()
	}

	if !reusedWarmActor {
		// CreateActor is idempotent here: on follow-up turns the actor was created
		// (and suspended) on a previous turn, so AlreadyExists is expected and fine.
		if _, err := h.ateClient.CreateActor(ctx, conversationID); err != nil && status.Code(err) != codes.AlreadyExists {
			return nil, fmt.Errorf("failed to create substrate actor %s: %w", conversationID, err)
		}

		// Resume the actor so it is scheduled onto a worker and gets a routable IP.
		resumeResp, err := h.ateClient.ResumeActor(ctx, conversationID)
		if err != nil {
			return nil, fmt.Errorf("failed to resume substrate actor %s: %w", conversationID, err)
		}
		actor := resumeResp.Actor
		if actor == nil {
			return nil, fmt.Errorf("received nil actor in response for %s", conversationID)
		}
		if actor.AteomPodIp == "" {
			return nil, fmt.Errorf("actor %s has no active worker IP address", conversationID)
		}
		workerAddr = fmt.Sprintf("%s:%d", actor.AteomPodIp, h.port)
		h.rememberWarmActor(conversationID, workerAddr)
	}

	// Establish connection to the actor's worker IP
	conn, err := grpc.NewClient(workerAddr, h.dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial remote harness service at %s: %w", workerAddr, err)
	}

	// Wait for the harness to be reachable and ready before handing back the
	// execution.
	if err := waitForHealthy(ctx, conn, healthCheckTimeout); err != nil {
		conn.Close()
		return nil, fmt.Errorf("harness for %s not ready at %s: %w", conversationID, workerAddr, err)
	}

	return &substrateExecution{
		harness:        h,
		conversationID: conversationID,
		execID:         uuid.NewString(),
		conn:           conn,
		client:         proto.NewHarnessServiceClient(conn),
		harnessConfig:  harnessConfig,
	}, nil
}

func (h *SubstrateHarness) beginWarmTurn(ctx context.Context, conversationID string) (string, bool, error) {
	if h.idleMode != idleModeWarmThenSuspend {
		return "", false, nil
	}

	for {
		h.idleMu.Lock()
		if h.warmActors == nil {
			h.warmActors = make(map[string]*warmActorState)
		}
		state := h.warmActors[conversationID]
		if state == nil {
			state = &warmActorState{inTurn: true}
			h.warmActors[conversationID] = state
			h.idleMu.Unlock()
			return "", false, nil
		}
		if state.suspending != nil {
			done := state.suspending
			h.idleMu.Unlock()
			select {
			case <-ctx.Done():
				return "", false, ctx.Err()
			case <-done:
				continue
			}
		}
		if state.inTurn {
			h.idleMu.Unlock()
			return "", false, fmt.Errorf("substrate actor %s already has an active turn", conversationID)
		}

		state.generation++
		if state.timer != nil {
			state.timer.Stop()
			state.timer = nil
		}
		state.inTurn = true
		workerAddr := state.workerAddr
		h.idleMu.Unlock()
		return workerAddr, workerAddr != "", nil
	}
}

func (h *SubstrateHarness) rememberWarmActor(conversationID, workerAddr string) {
	if h.idleMode != idleModeWarmThenSuspend {
		return
	}
	h.idleMu.Lock()
	defer h.idleMu.Unlock()
	state := h.warmActors[conversationID]
	if state != nil {
		state.workerAddr = workerAddr
	}
}

func (h *SubstrateHarness) abortWarmTurn(conversationID string) {
	if h.idleMode != idleModeWarmThenSuspend {
		return
	}
	h.idleMu.Lock()
	defer h.idleMu.Unlock()
	state := h.warmActors[conversationID]
	if state == nil {
		return
	}
	state.inTurn = false
	if state.workerAddr == "" {
		delete(h.warmActors, conversationID)
		return
	}
	state.generation++
	generation := state.generation
	state.timer = time.AfterFunc(h.idleTimeout, func() {
		h.suspendWarmActor(conversationID, "", generation)
	})
}

// waitForHealthy blocks until the harness behind conn reports SERVING via the
// standard gRPC health protocol until timeout. A harness that is reachable
// but does not implement the health service (Unimplemented) is treated as
// ready; connection failures (Unavailable) and NOT_SERVING are retried.
func waitForHealthy(ctx context.Context, conn *grpc.ClientConn, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := grpc_health_v1.NewHealthClient(conn)
	const maxBackoff = 2 * time.Second
	backoff := 100 * time.Millisecond
	for {
		resp, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: ""})
		if err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING {
			return nil
		}
		if status.Code(err) == codes.Unimplemented {
			// Reachable but no health service: the port is up, proceed.
			return nil
		}

		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("harness not healthy within %s: %w", timeout, err)
			}
			return fmt.Errorf("harness not healthy within %s (last status: %s)", timeout, resp.GetStatus())
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

type substrateExecution struct {
	harness        *SubstrateHarness
	conversationID string
	execID         string
	conn           *grpc.ClientConn
	client         proto.HarnessServiceClient
	harnessConfig  []byte

	mu      sync.Mutex
	pending []*proto.Message
}

func (e *substrateExecution) ID() string {
	return e.execID
}

func (e *substrateExecution) Queue(ctx context.Context, msg ...*proto.Message) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending = append(e.pending, msg...)
	return nil
}

func (e *substrateExecution) Run(ctx context.Context, handler harness.Handler) error {
	ctx, span := otel.Tracer("substrate-harness").Start(ctx, "Run")
	defer span.End()

	e.mu.Lock()
	inputs := e.pending
	e.pending = nil
	e.mu.Unlock()

	stream, err := e.client.Connect(ctx)
	if err != nil {
		return fmt.Errorf("failed to open harness service stream: %w", err)
	}

	// Send a HarnessRequest to initiate the turn.
	start := &proto.HarnessRequest{
		ConversationId: e.conversationID,
		HarnessId:      e.harness.harnessID,
		Type: &proto.HarnessRequest_Start{
			Start: &proto.HarnessStart{
				HarnessConfig: e.harnessConfig,
				Messages:      inputs,
			},
		},
	}
	// A server that fails before reading the start frame makes Send/CloseSend
	// report io.EOF; the real status is surfaced by DrainStream's Recv below, so
	// only treat non-EOF errors as send failures.
	if err := stream.Send(start); err != nil && err != io.EOF {
		return fmt.Errorf("failed to send harness start: %w", err)
	}

	// Close send direction to trigger server processing.
	if err := stream.CloseSend(); err != nil && err != io.EOF {
		return fmt.Errorf("failed to close stream send direction: %w", err)
	}

	// Drain HarnessResponse frames until the terminal HarnessEnd.
	return harness.DrainStream(ctx, stream, e.execID, handler)
}

func (e *substrateExecution) Close(ctx context.Context) error {
	if e.conn != nil {
		_ = e.conn.Close()
	}
	if e.harness.idleMode == idleModeWarmThenSuspend {
		e.harness.scheduleWarmSuspend(e.conversationID, e.execID)
		return nil
	}

	e.harness.suspendActor(ctx, e.conversationID, e.execID)
	return nil
}

func (h *SubstrateHarness) scheduleWarmSuspend(conversationID, execID string) {
	h.idleMu.Lock()
	defer h.idleMu.Unlock()
	state := h.warmActors[conversationID]
	if state == nil {
		return
	}
	state.inTurn = false
	state.generation++
	generation := state.generation
	state.timer = time.AfterFunc(h.idleTimeout, func() {
		h.suspendWarmActor(conversationID, execID, generation)
	})
}

func (h *SubstrateHarness) suspendWarmActor(conversationID, execID string, generation uint64) {
	h.idleMu.Lock()
	state := h.warmActors[conversationID]
	if state == nil || state.generation != generation || state.inTurn {
		h.idleMu.Unlock()
		return
	}
	state.timer = nil
	done := make(chan struct{})
	state.suspending = done
	h.idleMu.Unlock()

	h.suspendActor(context.Background(), conversationID, execID)

	h.idleMu.Lock()
	state = h.warmActors[conversationID]
	if state != nil && state.suspending == done {
		delete(h.warmActors, conversationID)
	}
	close(done)
	h.idleMu.Unlock()
}

func (h *SubstrateHarness) suspendActor(ctx context.Context, conversationID, execID string) {
	// Suspend actor to return resource to standard standby pool
	slog.InfoContext(ctx, "Suspending SubstrATE actor",
		slog.String("conversation_id", conversationID),
		slog.String("exec_id", execID),
	)
	suspendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := h.ateClient.SuspendActor(suspendCtx, conversationID); err != nil {
		slog.ErrorContext(ctx, "Failed to suspend SubstrATE actor",
			slog.String("conversation_id", conversationID),
			slog.Any("error", err),
		)
	}
}
