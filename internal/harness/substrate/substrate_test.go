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
	"bytes"
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/ax/internal/harness/harnesstest"
	"github.com/google/ax/internal/k8s/ate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

var substrateHarnessConfig = []byte(`{"model":"gemini-2.5-pro"}`)

// startHealthTestServer starts a gRPC server on a random local port. If hs is
// non-nil the standard health service is registered. Returns the listen address.
func startHealthTestServer(t *testing.T, hs *health.Server) string {
	t.Helper()
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	if hs != nil {
		grpc_health_v1.RegisterHealthServer(s, hs)
	}
	go func() {
		_ = s.Serve(lis)
	}()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func dialTestConn(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to dial %s: %v", addr, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestWaitForHealthy_Serving(t *testing.T) {
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	conn := dialTestConn(t, startHealthTestServer(t, hs))

	if err := waitForHealthy(context.Background(), conn, 5*time.Second); err != nil {
		t.Fatalf("expected healthy, got %v", err)
	}
}

func TestWaitForHealthy_UnimplementedProceeds(t *testing.T) {
	// Server is up but does not register the health service.
	conn := dialTestConn(t, startHealthTestServer(t, nil))

	if err := waitForHealthy(context.Background(), conn, 5*time.Second); err != nil {
		t.Fatalf("expected to proceed when health is unimplemented, got %v", err)
	}
}

func TestWaitForHealthy_TimesOut(t *testing.T) {
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	conn := dialTestConn(t, startHealthTestServer(t, hs))

	if err := waitForHealthy(context.Background(), conn, 500*time.Millisecond); err == nil {
		t.Fatal("expected timeout error while NOT_SERVING, got nil")
	}
}

func TestWaitForHealthy_StatusChange(t *testing.T) {
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	conn := dialTestConn(t, startHealthTestServer(t, hs))

	go func() {
		time.Sleep(150 * time.Millisecond)
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	}()

	if err := waitForHealthy(context.Background(), conn, 5*time.Second); err != nil {
		t.Fatalf("expected healthy after status flip, got %v", err)
	}
}

func TestWaitForHealthy_ServerDown(t *testing.T) {
	// Reserve a port then release it so nothing is listening there.
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()
	conn := dialTestConn(t, addr)

	if err := waitForHealthy(context.Background(), conn, 500*time.Millisecond); err == nil {
		t.Fatal("expected timeout error when server is down, got nil")
	}
}

func TestNewReadsWarmIdlePolicyFromEnvironment(t *testing.T) {
	t.Setenv("AX_SUBSTRATE_IDLE_MODE", "warm-then-suspend")
	t.Setenv("AX_SUBSTRATE_IDLE_TIMEOUT", "750ms")

	h, err := New("antigravity", "127.0.0.1:1", "ax", "antigravity-template", 50053)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.ateClient.Close() })
	if h.idleMode != idleModeWarmThenSuspend {
		t.Fatalf("idle mode = %v, want warm-then-suspend", h.idleMode)
	}
	if h.idleTimeout != 750*time.Millisecond {
		t.Fatalf("idle timeout = %v, want 750ms", h.idleTimeout)
	}
}

func TestNewDefaultsToImmediateSuspend(t *testing.T) {
	t.Setenv("AX_SUBSTRATE_IDLE_MODE", "")
	t.Setenv("AX_SUBSTRATE_IDLE_TIMEOUT", "")

	h, err := New("antigravity", "127.0.0.1:1", "ax", "antigravity-template", 50053)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.ateClient.Close() })
	if h.idleMode != idleModeImmediateSuspend || h.idleTimeout != 0 {
		t.Fatalf("idle policy = (%v, %v), want immediate-suspend", h.idleMode, h.idleTimeout)
	}
}

func TestNewRejectsInvalidIdlePolicy(t *testing.T) {
	for _, mode := range []string{"keep-forever", "pause-resume"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AX_SUBSTRATE_IDLE_MODE", mode)

			if _, err := New("antigravity", "127.0.0.1:1", "ax", "antigravity-template", 50053); err == nil {
				t.Fatalf("New accepted invalid AX_SUBSTRATE_IDLE_MODE %q", mode)
			}
		})
	}
}

// newTestSubstrateHarness builds a SubstrateHarness wired to the mock control
// server and the mock harness server. It constructs the struct directly (rather
// than via NewSubstrateHarness) so the control client can use insecure
// credentials instead of the TLS that NewSubstrateHarness hard-codes.
func newTestSubstrateHarness(t *testing.T, ctrlAddr, harnessAddr string) *SubstrateHarness {
	t.Helper()
	_, portStr, err := net.SplitHostPort(harnessAddr)
	if err != nil {
		t.Fatalf("bad harness addr %q: %v", harnessAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("bad harness port %q: %v", portStr, err)
	}
	client, err := ate.NewClient("ax", "antigravity-template", ctrlAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to create ate client: %v", err)
	}
	return &SubstrateHarness{
		harnessID: "antigravity",
		ateClient: client,
		port:      port,
		dialOpts:  []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
	}
}

// Test full SubstrateHarness Start -> Run -> Close flow against the shared
// in-process mocks (see mocks_test.go).
// They lock in the wiring that a substrate bump or an ax-side change could silently
// break: create/resume idempotency, worker-IP extraction, the health gate, the
// Connect streaming protocol, and suspend-on-close.
func TestSubstrateHarness_EndToEnd(t *testing.T) {
	ctrl := &harnesstest.MockControlServer{ResumeIP: "127.0.0.1"}
	srv := &harnesstest.MockHarnessServer{}
	h := newTestSubstrateHarness(t, harnesstest.StartControlServer(t, ctrl), harnesstest.StartHarnessServer(t, srv))

	ctx := context.Background()
	exec, err := h.Start(ctx, "conv-1", substrateHarnessConfig)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := exec.Queue(ctx, harnesstest.UserText("hi")); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	handler := &harnesstest.MockHandler{}
	if err := exec.Run(ctx, handler); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The harness server received the start frame with the right identifiers.
	convID, harnessID, harnessConfig, inputs := srv.Received()
	if convID != "conv-1" || harnessID != "antigravity" {
		t.Errorf("server got convID=%q harnessID=%q, want conv-1/antigravity", convID, harnessID)
	}
	if !bytes.Equal(harnessConfig, substrateHarnessConfig) {
		t.Errorf("server got harnessConfig=%q, want %q", harnessConfig, substrateHarnessConfig)
	}
	if !slices.Equal(inputs, []string{"hi"}) {
		t.Errorf("server got inputs=%v, want [hi]", inputs)
	}

	// The handler streamed the output and completed.
	if !handler.IsDone() {
		t.Error("handler did not complete")
	}
	if got := handler.Texts(); !slices.Equal(got, []string{"ack: hi"}) {
		t.Errorf("handler messages=%v, want [ack: hi]", got)
	}

	// CreateActor then ResumeActor ran for the conversation; no suspend yet.
	create, resume, suspend := ctrl.Calls()
	want := []string{"conv-1"}
	if !slices.Equal(create, want) || !slices.Equal(resume, want) {
		t.Errorf("create=%v resume=%v, want %v each", create, resume, want)
	}
	if len(suspend) != 0 {
		t.Errorf("suspend called before Close: %v", suspend)
	}

	// Close suspends the actor.
	if err := exec.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, suspend = ctrl.Calls(); !slices.Equal(suspend, want) {
		t.Errorf("suspend=%v, want %v", suspend, want)
	}
}

func TestSubstrateHarness_WarmThenSuspendReusesActorUntilIdle(t *testing.T) {
	ctrl := &harnesstest.MockControlServer{ResumeIP: "127.0.0.1"}
	srv := &harnesstest.MockHarnessServer{}
	h := newTestSubstrateHarness(t, harnesstest.StartControlServer(t, ctrl), harnesstest.StartHarnessServer(t, srv))
	h.idleMode = idleModeWarmThenSuspend
	h.idleTimeout = 200 * time.Millisecond

	runTurn := func(input string) {
		t.Helper()
		ctx := context.Background()
		exec, err := h.Start(ctx, "conv-warm", substrateHarnessConfig)
		if err != nil {
			t.Fatalf("Start(%q): %v", input, err)
		}
		if err := exec.Queue(ctx, harnesstest.UserText(input)); err != nil {
			t.Fatalf("Queue(%q): %v", input, err)
		}
		if err := exec.Run(ctx, &harnesstest.MockHandler{}); err != nil {
			t.Fatalf("Run(%q): %v", input, err)
		}
		if err := exec.Close(ctx); err != nil {
			t.Fatalf("Close(%q): %v", input, err)
		}
	}

	runTurn("one")
	runTurn("two")

	create, resume, suspend := ctrl.Calls()
	if !slices.Equal(create, []string{"conv-warm"}) {
		t.Fatalf("create=%v, want one actor creation", create)
	}
	if !slices.Equal(resume, []string{"conv-warm"}) {
		t.Fatalf("resume=%v, want one cold resume", resume)
	}
	if len(suspend) != 0 {
		t.Fatalf("suspend called before the final idle timeout: %v", suspend)
	}
	if got := srv.ConnectCalls(); got != 2 {
		t.Fatalf("HarnessService Connect calls = %d, want one fresh stream per turn", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, suspend = ctrl.Calls()
		if slices.Equal(suspend, []string{"conv-warm"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("suspend=%v, want one call after idle timeout", suspend)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubstrateHarness_StaleWarmTimerCannotSuspendActiveTurn(t *testing.T) {
	ctrl := &harnesstest.MockControlServer{ResumeIP: "127.0.0.1"}
	srv := &harnesstest.MockHarnessServer{}
	h := newTestSubstrateHarness(t, harnesstest.StartControlServer(t, ctrl), harnesstest.StartHarnessServer(t, srv))
	h.idleMode = idleModeWarmThenSuspend
	h.idleTimeout = 100 * time.Millisecond

	ctx := context.Background()
	first, err := h.Start(ctx, "conv-generation", substrateHarnessConfig)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := first.Run(ctx, &harnesstest.MockHandler{}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := h.Start(ctx, "conv-generation", substrateHarnessConfig)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	time.Sleep(2 * h.idleTimeout)
	if _, _, suspend := ctrl.Calls(); len(suspend) != 0 {
		t.Fatalf("stale timer suspended an active turn: %v", suspend)
	}
	if err := second.Run(ctx, &harnesstest.MockHandler{}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if err := second.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, suspend := ctrl.Calls()
		if slices.Equal(suspend, []string{"conv-generation"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("suspend=%v, want one call after the active turn closed", suspend)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubstrateHarness_WarmStartFailureStillSchedulesSuspend(t *testing.T) {
	ctrl := &harnesstest.MockControlServer{ResumeIP: "127.0.0.1"}
	h := newTestSubstrateHarness(t, harnesstest.StartControlServer(t, ctrl), "127.0.0.1:1")
	h.idleMode = idleModeWarmThenSuspend
	h.idleTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := h.Start(ctx, "conv-start-failure", substrateHarnessConfig); err == nil {
		t.Fatal("Start unexpectedly succeeded against an unavailable harness")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, suspend := ctrl.Calls()
		if slices.Equal(suspend, []string{"conv-start-failure"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("suspend=%v, want failed warm Start to retain idle cleanup", suspend)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubstrateHarness_CreateAlreadyExistsTolerated(t *testing.T) {
	ctrl := &harnesstest.MockControlServer{
		ResumeIP:  "127.0.0.1",
		CreateErr: status.Error(codes.AlreadyExists, "exists"),
	}
	h := newTestSubstrateHarness(t, harnesstest.StartControlServer(t, ctrl), harnesstest.StartHarnessServer(t, &harnesstest.MockHarnessServer{}))

	ctx := context.Background()
	exec, err := h.Start(ctx, "conv-1", substrateHarnessConfig)
	if err != nil {
		t.Fatalf("Start should tolerate AlreadyExists: %v", err)
	}
	t.Cleanup(func() { _ = exec.Close(ctx) })

	if err := exec.Queue(ctx, harnesstest.UserText("hi")); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	handler := &harnesstest.MockHandler{}
	if err := exec.Run(ctx, handler); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !handler.IsDone() {
		t.Error("handler did not complete")
	}
	if _, resume, _ := ctrl.Calls(); !slices.Equal(resume, []string{"conv-1"}) {
		t.Errorf("resume=%v, want [conv-1]", resume)
	}
}

func TestSubstrateHarness_ResumeNoWorkerIP(t *testing.T) {
	ctrl := &harnesstest.MockControlServer{ResumeIP: ""} // empty AteomPodIp
	h := newTestSubstrateHarness(t, harnesstest.StartControlServer(t, ctrl), harnesstest.StartHarnessServer(t, &harnesstest.MockHarnessServer{}))

	_, err := h.Start(context.Background(), "conv-1", substrateHarnessConfig)
	if err == nil {
		t.Fatal("expected error for empty worker IP, got nil")
	}
	if !strings.Contains(err.Error(), "no active worker IP") {
		t.Errorf("error = %v, want it to mention 'no active worker IP'", err)
	}
}

func TestSubstrateHarness_ResumeNilActor(t *testing.T) {
	ctrl := &harnesstest.MockControlServer{ResumeNilActor: true}
	h := newTestSubstrateHarness(t, harnesstest.StartControlServer(t, ctrl), harnesstest.StartHarnessServer(t, &harnesstest.MockHarnessServer{}))

	_, err := h.Start(context.Background(), "conv-1", substrateHarnessConfig)
	if err == nil {
		t.Fatal("expected error for nil actor, got nil")
	}
	if !strings.Contains(err.Error(), "nil actor") {
		t.Errorf("error = %v, want it to mention 'nil actor'", err)
	}
}

func TestSubstrateHarness_HarnessFailedFrame(t *testing.T) {
	ctrl := &harnesstest.MockControlServer{ResumeIP: "127.0.0.1"}
	srv := &harnesstest.MockHarnessServer{FailFrame: true, ErrCode: 13, ErrMessage: "boom"}
	h := newTestSubstrateHarness(t, harnesstest.StartControlServer(t, ctrl), harnesstest.StartHarnessServer(t, srv))

	ctx := context.Background()
	exec, err := h.Start(ctx, "conv-1", substrateHarnessConfig)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = exec.Close(ctx) })
	if err := exec.Queue(ctx, harnesstest.UserText("hi")); err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if err := exec.Run(ctx, &harnesstest.MockHandler{}); err == nil {
		t.Fatal("expected error from failed harness frame, got nil")
	} else if !strings.Contains(err.Error(), "harness failed") {
		t.Errorf("error = %v, want it to mention 'harness failed'", err)
	}
}
