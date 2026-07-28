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

package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/google/ax/internal/controller"
	"github.com/google/ax/internal/controller/eventlog"
	"github.com/google/ax/internal/controller/eventlog/eventlogtest"
	"github.com/google/ax/internal/harness"
	"github.com/google/ax/proto"
)

// fakeSuspenderHarness implements the optional controller.ConversationSuspender
// capability so SuspendConversation's gRPC-layer error mapping can be
// exercised directly against a real *Server, without a transport.
type fakeSuspenderHarness struct {
	harness.Harness
	err error
}

func (f *fakeSuspenderHarness) SuspendConversation(_ context.Context, _ string) error {
	return f.err
}

// newTestServer builds a real *Server around a controller with a single
// registered harness, mirroring the newTestController fixture in
// internal/controller/controller_test.go.
func newTestServer(t *testing.T, h harness.Harness) *Server {
	t.Helper()
	reg := controller.NewRegistry()
	if err := reg.RegisterHarness("test-harness", h); err != nil {
		t.Fatal(err)
	}
	log := &eventlogtest.MemoryEventLog{}
	c, err := controller.New(context.Background(), controller.Config{
		Registry:        reg,
		EventLogBuilder: func() (eventlog.EventLog, error) { return log, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return New(c)
}

// The handler must map harness.ErrConversationInTurn to FailedPrecondition:
// downstream consumers switch on this code to decide whether to retry later.
func TestSuspendConversation_InTurnMapsToFailedPrecondition(t *testing.T) {
	s := newTestServer(t, &fakeSuspenderHarness{err: harness.ErrConversationInTurn})

	_, err := s.SuspendConversation(context.Background(), &proto.SuspendConversationRequest{ConversationId: "conv-1"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("status.Code(err) = %v, want %v", got, codes.FailedPrecondition)
	}
}

// Any other harness error must map to Internal, not FailedPrecondition: the
// consumer's retry logic depends on the two codes staying distinct.
func TestSuspendConversation_OtherErrorMapsToInternal(t *testing.T) {
	s := newTestServer(t, &fakeSuspenderHarness{err: errBoom})

	_, err := s.SuspendConversation(context.Background(), &proto.SuspendConversationRequest{ConversationId: "conv-1"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("status.Code(err) = %v, want %v", got, codes.Internal)
	}
}

// A capable harness that succeeds must produce no error and a response.
func TestSuspendConversation_SuccessReturnsResponse(t *testing.T) {
	s := newTestServer(t, &fakeSuspenderHarness{})

	resp, err := s.SuspendConversation(context.Background(), &proto.SuspendConversationRequest{ConversationId: "conv-1"})
	if err != nil {
		t.Fatalf("SuspendConversation: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a non-nil response")
	}
}

// An empty conversation_id must be rejected before the controller is
// consulted at all.
func TestSuspendConversation_EmptyIDIsInvalidArgument(t *testing.T) {
	s := newTestServer(t, &fakeSuspenderHarness{})

	_, err := s.SuspendConversation(context.Background(), &proto.SuspendConversationRequest{ConversationId: ""})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("status.Code(err) = %v, want %v", got, codes.InvalidArgument)
	}
}

var errBoom = errBoomError{}

// errBoomError is a distinct error type (not harness.ErrConversationInTurn)
// used to exercise the handler's default Internal-mapping branch.
type errBoomError struct{}

func (errBoomError) Error() string { return "boom" }
