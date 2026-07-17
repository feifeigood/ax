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

// defaultWarmProbeTimeout bounds the health probe against a reused warm
// address: the actor was serving moments ago, so unreachability within this
// window means it is gone and the cold resume path should take over.
const defaultWarmProbeTimeout = 5 * time.Second

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

	idleMode         idleMode
	idleTimeout      time.Duration
	warmProbeTimeout time.Duration
	idleMu           sync.Mutex
	warmActors       map[string]*warmActorState
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
		harnessID:        harnessID,
		ateClient:        client,
		port:             port,
		dialOpts:         opts,
		idleMode:         idleMode,
		idleTimeout:      idleTimeout,
		warmProbeTimeout: defaultWarmProbeTimeout,
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

	if reusedWarmActor {
		// The cached address only proves that this process previously observed a
		// running actor. Resolve through the authoritative control path before
		// connecting because a worker IP can be reassigned while the actor is warm.
		cachedWorkerAddr := workerAddr
		workerAddr, err = h.resumeWorkerAddr(ctx, conversationID)
		if err != nil {
			return nil, err
		}
		h.rememberWarmActor(conversationID, workerAddr)

		exec, probeErr := h.connect(ctx, conversationID, harnessConfig, workerAddr, h.probeTimeout())
		if probeErr == nil {
			return exec, nil
		}
		if ctx.Err() != nil {
			return nil, probeErr
		}
		slog.WarnContext(ctx, "Warm SubstrATE actor unreachable; restarting before cold resume",
			slog.String("conversation_id", conversationID),
			slog.String("worker_addr", workerAddr),
			slog.String("cached_worker_addr", cachedWorkerAddr),
			slog.Any("error", probeErr),
		)
		// ResumeActor is a no-op for an actor already marked RUNNING. Suspend it
		// first so the cold path below performs a real restore. Keep the cached
		// address until this succeeds so a failed reset still gets idle cleanup.
		resetCtx, cancelReset := context.WithTimeout(ctx, 10*time.Second)
		_, suspendErr := h.ateClient.SuspendActor(resetCtx, conversationID)
		cancelReset()
		if suspendErr != nil {
			return nil, fmt.Errorf("failed to reset unreachable substrate actor %s after %v: %w", conversationID, probeErr, suspendErr)
		}
		h.forgetWarmAddr(conversationID)
	}

	// CreateActor is idempotent here: on follow-up turns the actor was created
	// (and suspended) on a previous turn, so AlreadyExists is expected and fine.
	if _, err := h.ateClient.CreateActor(ctx, conversationID); err != nil && status.Code(err) != codes.AlreadyExists {
		return nil, fmt.Errorf("failed to create substrate actor %s: %w", conversationID, err)
	}

	// Resume the actor so it is scheduled onto a worker and gets a routable IP.
	workerAddr, err = h.resumeWorkerAddr(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	h.rememberWarmActor(conversationID, workerAddr)

	return h.connect(ctx, conversationID, harnessConfig, workerAddr, healthCheckTimeout)
}

// resumeWorkerAddr resolves the current actor through ATE and returns its
// authoritative worker address. ResumeActor is idempotent for RUNNING actors,
// so warm turns pay only the control-plane check and do not restore the actor.
func (h *SubstrateHarness) resumeWorkerAddr(ctx context.Context, conversationID string) (string, error) {
	resumeResp, err := h.ateClient.ResumeActor(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("failed to resume substrate actor %s: %w", conversationID, err)
	}
	actor := resumeResp.GetActor()
	if actor == nil {
		return "", fmt.Errorf("received nil actor in response for %s", conversationID)
	}
	if actor.GetActorId() != conversationID {
		return "", fmt.Errorf("received actor %s while resuming %s", actor.GetActorId(), conversationID)
	}
	if actor.GetAteomPodIp() == "" {
		return "", fmt.Errorf("actor %s has no active worker IP address", conversationID)
	}
	return fmt.Sprintf("%s:%d", actor.GetAteomPodIp(), h.port), nil
}

// connect dials the actor's worker address and waits for the harness to be
// reachable and ready before handing back the execution.
func (h *SubstrateHarness) connect(ctx context.Context, conversationID string, harnessConfig []byte, workerAddr string, healthTimeout time.Duration) (harness.Execution, error) {
	conn, err := grpc.NewClient(workerAddr, h.dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial remote harness service at %s: %w", workerAddr, err)
	}

	if err := waitForHealthy(ctx, conn, healthTimeout); err != nil {
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

func (h *SubstrateHarness) probeTimeout() time.Duration {
	if h.warmProbeTimeout > 0 {
		return h.warmProbeTimeout
	}
	return defaultWarmProbeTimeout
}

// forgetWarmAddr drops the cached worker address for an in-turn conversation
// after a failed reuse probe, so the cold path re-resolves it and a failed
// turn deletes the entry instead of re-arming a timer around a dead address.
func (h *SubstrateHarness) forgetWarmAddr(conversationID string) {
	h.idleMu.Lock()
	defer h.idleMu.Unlock()
	if state := h.warmActors[conversationID]; state != nil {
		state.workerAddr = ""
	}
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
	h.endWarmTurnLocked(state, conversationID, "")
}

// endWarmTurnLocked marks the conversation's turn ended and either drops the
// entry (no usable worker address) or arms the idle suspend timer. The caller
// must hold idleMu.
func (h *SubstrateHarness) endWarmTurnLocked(state *warmActorState, conversationID, execID string) {
	state.inTurn = false
	if state.workerAddr == "" {
		delete(h.warmActors, conversationID)
		return
	}
	state.generation++
	generation := state.generation
	state.timer = time.AfterFunc(h.idleTimeout, func() {
		h.suspendWarmActor(conversationID, execID, generation)
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

// CloseBeforeNextStart implements the controller's optional eager-close
// capability. Warm mode tracks a per-conversation turn slot, so this execution
// must be closed before another Start for the same conversation; immediate
// mode keeps upstream's deferred-close semantics.
func (e *substrateExecution) CloseBeforeNextStart() bool {
	return e.harness.idleMode == idleModeWarmThenSuspend
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
	h.endWarmTurnLocked(state, conversationID, execID)
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

// Shutdown drains warm actors awaiting idle suspension so a process exit does
// not leak them as RUNNING actors. A warm actor sits between turns with a
// pending idle timer whose only home is this process's memory; if the process
// dies before the timer fires, the actor is never suspended. Shutdown stops each
// pending timer and suspends the actor synchronously.
//
// Actors with an active turn are left untouched: the turn owns the actor and
// schedules its own suspension on Close. Callers should therefore invoke
// Shutdown only after in-flight turns have drained (e.g. after the gRPC server's
// GracefulStop returns) so no turn re-arms a timer after this drain.
func (h *SubstrateHarness) Shutdown(ctx context.Context) {
	if h.idleMode != idleModeWarmThenSuspend {
		return
	}

	h.idleMu.Lock()
	var (
		drain      []string
		inProgress []chan struct{}
	)
	for conversationID, state := range h.warmActors {
		switch {
		case state.inTurn:
			// An active turn owns the actor; it will suspend on Close.
			continue
		case state.suspending != nil:
			// A fired timer is already suspending this actor; wait for it below.
			inProgress = append(inProgress, state.suspending)
		case state.timer != nil:
			state.timer.Stop()
			state.timer = nil
			// Neutralize a timer callback that already fired but is still blocked
			// on idleMu: the generation bump makes suspendWarmActor a no-op so it
			// cannot suspend again after we do.
			state.generation++
			if state.workerAddr == "" {
				delete(h.warmActors, conversationID)
				continue
			}
			drain = append(drain, conversationID)
		default:
			// No timer and not suspending: nothing deferred to clean up.
			delete(h.warmActors, conversationID)
		}
	}
	h.idleMu.Unlock()

	for _, conversationID := range drain {
		h.suspendActor(ctx, conversationID, "")
		h.idleMu.Lock()
		delete(h.warmActors, conversationID)
		h.idleMu.Unlock()
	}

	// Wait for any timer-driven suspensions already in progress so they are not
	// cut short by process exit.
	for _, done := range inProgress {
		select {
		case <-ctx.Done():
			return
		case <-done:
		}
	}
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
