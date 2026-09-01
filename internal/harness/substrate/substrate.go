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
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/google/ax/internal/harness"
	"github.com/google/ax/internal/ate"
	"github.com/google/ax/proto"
	"github.com/google/uuid"
)

// Compile-time interface assertions.
var _ harness.Harness = (*SubstrateHarness)(nil)
var _ harness.Execution = (*substrateExecution)(nil)

// healthCheckTimeout defines the maximum time Start waits for a freshly
// created/resumed actor's harness to become reachable and ready.
const healthCheckTimeout = 60 * time.Second

// defaultRouterAddr is the in-cluster address of the atenet-router Service,
// the single ingress for all actor traffic. Since substrate removed the
// worker pod's compatibility DNAT, actors are only reachable through the
// router's Envoy (h2c on the plain HTTP port), which resolves the target
// from the request :authority.
const defaultRouterAddr = "atenet-router.ate-system.svc:80"

// actorDNSSuffix is substrate's actor addressing zone. The router derives
// (atespace, actor) from an :authority of the form
// "<actor>.<atespace>.actors.resources.substrate.ate.dev"; resolving that
// name in DNS is optional, the router only reads the header.
const actorDNSSuffix = "actors.resources.substrate.ate.dev"

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
	harnessID  string
	ateClient  *ate.Client
	namespace  string
	port       int
	routerAddr string
	dialOpts   []grpc.DialOption

	idleMode         idleMode
	idleTimeout      time.Duration
	warmProbeTimeout time.Duration
	idleMu           sync.Mutex
	warmActors       map[string]*warmActorState
}

// ateapiTokenFileEnv names the file holding a projected Kubernetes
// ServiceAccount token to present to the substrate Control API. Empty means the
// connection carries no credentials, which is only viable against a substrate
// revision that authenticates callers purely at the transport layer.
const ateapiTokenFileEnv = "AX_SUBSTRATE_ATEAPI_TOKEN_FILE"

// ateapiTokenFile authenticates to the substrate Control API with a bearer
// token read from a file.
//
// The Control API accepts either an mTLS client certificate or a Kubernetes
// ServiceAccount JWT whose audience matches the server's configured value, and
// it rejects a call carrying neither. This client dials with a server-only TLS
// configuration, so the token is what identifies it.
//
// The file is read per call rather than cached because the projected token is
// rotated in place by the kubelet; a token read once at dial time would expire
// while the connection stayed open.
type ateapiTokenFile string

func (f ateapiTokenFile) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	raw, err := os.ReadFile(string(f))
	if err != nil {
		return nil, fmt.Errorf("reading ateapi token from %s: %w", string(f), err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, fmt.Errorf("ateapi token file %s is empty", string(f))
	}
	return map[string]string{"authorization": "Bearer " + token}, nil
}

// RequireTransportSecurity reports true: the token is a bearer credential and
// must never be sent over a plaintext connection.
func (ateapiTokenFile) RequireTransportSecurity() bool { return true }

// New creates a new SubstrateHarness. routerAddr is the atenet-router ingress
// the harness connections are dialed through; empty selects the in-cluster
// default.
func New(harnessID string, endpoint string, namespace string, template string, port int, routerAddr string, opts ...grpc.DialOption) (*SubstrateHarness, error) {
	idleMode, idleTimeout, err := idlePolicyFromEnv()
	if err != nil {
		return nil, err
	}
	if port == 0 {
		port = 80 // Default HarnessService port; the actor-side target the router forwards to.
	}
	if routerAddr == "" {
		routerAddr = defaultRouterAddr
	}
	if namespace == "" {
		namespace = "ax"
	}
	if template == "" {
		template = "ax-harness-antigravity-template"
	}
	controlOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})),
	}
	if tokenFile := os.Getenv(ateapiTokenFileEnv); tokenFile != "" {
		controlOpts = append(controlOpts, grpc.WithPerRPCCredentials(ateapiTokenFile(tokenFile)))
	}
	client, err := ate.NewClient(namespace, template, endpoint, controlOpts...)
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
		namespace:        namespace,
		port:             port,
		routerAddr:       routerAddr,
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

// resumeRetryBudget bounds how long a Start waits out transient resume
// failures before failing the turn. Only the wait between attempts is
// budgeted; an in-flight ResumeActor keeps the caller's own deadline, since a
// legitimate cold restore can be slow.
const resumeRetryBudget = 30 * time.Second

// resumeRetryBaseDelay is the wait before the first resume retry; each
// subsequent wait grows 1.5x (plus up to 50% jitter) and is capped at 2s.
const resumeRetryBaseDelay = 100 * time.Millisecond

// resumeActor calls ResumeActor, retrying the three transient conditions the
// router's request parking also retries: a momentarily saturated worker pool
// (ResourceExhausted), a concurrent resume of the same actor (Aborted), and a
// control-plane blip (Unavailable). Anything else fails immediately.
func (h *SubstrateHarness) resumeActor(ctx context.Context, conversationID string) (*ateapipb.ResumeActorResponse, error) {
	start := time.Now()
	delay := resumeRetryBaseDelay
	for {
		resp, err := h.ateClient.ResumeActor(ctx, conversationID)
		if err == nil {
			return resp, nil
		}
		switch status.Code(err) {
		case codes.ResourceExhausted, codes.Aborted, codes.Unavailable:
		default:
			return nil, err
		}
		wait := delay + rand.N(delay/2)
		if time.Since(start)+wait > resumeRetryBudget {
			return nil, fmt.Errorf("retry budget exhausted after %s: %w", time.Since(start).Round(time.Millisecond), err)
		}
		slog.InfoContext(ctx, "Retrying transient substrate resume failure",
			slog.String("conversation_id", conversationID),
			slog.String("code", status.Code(err).String()),
			slog.Duration("wait", wait),
		)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("canceled while retrying transient resume failure: %w", err)
		case <-timer.C:
		}
		delay = min(delay*3/2, 2*time.Second)
	}
}

// resumeWorkerAddr resumes the actor through ATE, confirms it holds a worker
// assignment, and returns the address to dial — always the atenet-router
// ingress, which routes to the actor's current worker by :authority.
// ResumeActor is idempotent for RUNNING actors, so warm turns pay only the
// control-plane check and do not restore the actor.
func (h *SubstrateHarness) resumeWorkerAddr(ctx context.Context, conversationID string) (string, error) {
	resumeResp, err := h.resumeActor(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("failed to resume substrate actor %s: %w", conversationID, err)
	}
	actor := resumeResp.GetActor()
	if actor == nil {
		return "", fmt.Errorf("received nil actor in response for %s", conversationID)
	}
	if actor.GetMetadata().GetName() != conversationID {
		return "", fmt.Errorf("received actor %s while resuming %s", actor.GetMetadata().GetName(), conversationID)
	}
	if actor.GetStatus().GetWorkerAssignment().GetWorkerPodIp() == "" {
		return "", fmt.Errorf("actor %s has no worker assignment after resume (state %s)", conversationID, actor.GetStatus().GetState())
	}
	return h.routerAddr, nil
}

// actorAuthority is the :authority the router resolves to this conversation's
// actor. A non-default port rides along; the router forwards it to the actor
// as the target port.
func (h *SubstrateHarness) actorAuthority(conversationID string) string {
	authority := fmt.Sprintf("%s.%s.%s", conversationID, h.namespace, actorDNSSuffix)
	if h.port != 80 {
		authority = fmt.Sprintf("%s:%d", authority, h.port)
	}
	return authority
}

// connect dials the actor through the router ingress and waits for the
// harness to be reachable and ready before handing back the execution. The
// per-conversation :authority is what selects the actor.
func (h *SubstrateHarness) connect(ctx context.Context, conversationID string, harnessConfig []byte, workerAddr string, healthTimeout time.Duration) (harness.Execution, error) {
	dialOpts := append(append([]grpc.DialOption(nil), h.dialOpts...), grpc.WithAuthority(h.actorAuthority(conversationID)))
	conn, err := grpc.NewClient(workerAddr, dialOpts...)
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

// SuspendConversation releases conversationID's actor on demand instead of
// waiting for the idle timer. It is the harness half of the generic
// ConversationService.SuspendConversation RPC.
//
//   - idle warm entry  → disarm the timer and suspend through the standard
//     suspendWarmActor path (generation bump neutralizes a fired-but-blocked
//     timer callback, exactly as Shutdown does), then re-read the map before
//     confirming: suspendWarmActor releases parked beginWarmTurn waiters, and
//     a waiter that re-entered the conversation must not have its live actor
//     suspended out from under it (→ harness.ErrConversationInTurn instead)
//   - entry in a turn  → harness.ErrConversationInTurn; nothing is released
//   - suspend already in flight → wait for it, then verify the outcome the
//     same way the idle branch does: the in-flight suspend swallows its own
//     error and drops the entry either way, so its completion proves nothing
//   - no entry         → suspend directly: the actor is either already
//     suspended (SuspendActor fast-forwards, see substrate's MarkSuspending
//     IsComplete), never started (NotFound → success), or leaked by a previous
//     process life — the case timers can never cover.
//
// Those states only exist in warm-then-suspend mode. In immediate-suspend mode
// (the default) every turn suspends its own actor on Close, so there is no
// between-turn warm state, no turn bookkeeping, and therefore no in-turn guard
// here: the request goes straight to the control plane and turn exclusion rests
// entirely on the server's per-conversation in-flight guard.
func (h *SubstrateHarness) SuspendConversation(ctx context.Context, conversationID string) error {
	if conversationID == "" {
		return errors.New("conversation_id is required")
	}
	if h.idleMode != idleModeWarmThenSuspend {
		return h.suspendUntracked(ctx, conversationID)
	}
	h.idleMu.Lock()
	state := h.warmActors[conversationID]
	if state == nil {
		h.idleMu.Unlock()
		return h.suspendUntracked(ctx, conversationID)
	}
	if state.inTurn {
		h.idleMu.Unlock()
		return harness.ErrConversationInTurn
	}
	if state.suspending != nil {
		done := state.suspending
		h.idleMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
		// The in-flight suspend swallows its own SuspendActor error and drops
		// the warm entry either way (suspendWarmActor), so its completion
		// proves nothing about the actor. Verify the outcome: if it succeeded
		// this fast-forwards on an already-suspended actor; if it failed this
		// is the retry that keeps the caller from deleting its retry record for
		// an actor that is still RUNNING.
		return h.suspendUntracked(ctx, conversationID)
	}
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	state.generation++
	generation := state.generation
	h.idleMu.Unlock()

	h.suspendWarmActor(conversationID, "", generation)

	// suspendWarmActor's close(done) releases any beginWarmTurn parked on the
	// suspending channel, and that waiter immediately re-creates the entry with
	// inTurn set and cold-starts the actor. Re-read the map before touching the
	// control plane again: any entry at all (in a turn, parked, or freshly idle
	// after a whole turn slipped through) means the conversation was re-entered
	// and this actor is no longer ours to release. ErrConversationInTurn is
	// right in every one of those sub-cases — the caller keeps its record and
	// retries once the conversation goes idle again.
	h.idleMu.Lock()
	_, reentered := h.warmActors[conversationID]
	h.idleMu.Unlock()
	if reentered {
		return harness.ErrConversationInTurn
	}

	// suspendWarmActor swallows the ateClient.SuspendActor error (it is shared
	// with the timer path and Shutdown's drain, neither of which has anywhere
	// to report a failure to). Re-issue the suspend here so a genuine failure
	// surfaces to the caller instead of being reported as success. If
	// suspendWarmActor's own call already succeeded, substrate's suspend
	// workflow fast-forwards on an already-suspended actor (MarkSuspendingStep
	// and CallAteletSuspendStep both treat STATUS_SUSPENDED as complete), so
	// this is a cheap control-plane no-op, not a second real suspend.
	//
	// suspendWarmActor's own call runs on its own hardcoded deadline, not the
	// caller's, so if it times out while ateapi is still running the workflow
	// the re-issue meets the held per-actor lock and comes back Aborted. That
	// false negative self-corrects on the caller's next sweep, which finds no
	// warm entry and confirms the (by then finished) suspend directly.
	return h.suspendUntracked(ctx, conversationID)
}

// suspendUntracked suspends an actor this process holds no warm state for.
// NotFound is success: no actor means nothing occupies a worker.
//
// Only NotFound. An actor substrate refuses to suspend because it is not
// RUNNING — CRASHED above all, but also PAUSED — fails every attempt, so the
// caller can never reach success for it and cannot tell "retry me" from "no
// suspend will ever work here". Its retry backoff bounds the cost; mapping
// ateapi's FailedPrecondition to a distinct terminal outcome is a known
// follow-up.
func (h *SubstrateHarness) suspendUntracked(ctx context.Context, conversationID string) error {
	if _, err := h.ateClient.SuspendActor(ctx, conversationID); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("suspend substrate actor %s: %w", conversationID, err)
	}
	return nil
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
