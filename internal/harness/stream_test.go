// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package harness_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/ax/internal/harness"
	"github.com/google/ax/internal/harness/harnesstest"
	"github.com/google/ax/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type terminalMetadataHandler struct {
	completed bool
	metadata  []byte
}

func (h *terminalMetadataHandler) OnMessage(context.Context, string, *proto.Message) error {
	return nil
}

func (h *terminalMetadataHandler) OnComplete(context.Context, string) error {
	h.completed = true
	return nil
}

func (h *terminalMetadataHandler) OnCompleteWithMetadata(_ context.Context, _ string, metadata []byte) error {
	h.metadata = append([]byte(nil), metadata...)
	return nil
}

func TestDrainStreamDispatchesTerminalHarnessMetadata(t *testing.T) {
	want := []byte("agentfleet-metadata-fixture")
	addr := harnesstest.StartHarnessServer(t, &harnesstest.MockHarnessServer{
		HarnessMetadata: want,
	})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx := context.Background()
	stream, err := proto.NewHarnessServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := stream.Send(&proto.HarnessRequest{
		ConversationId: "conv-1",
		Type: &proto.HarnessRequest_Start{
			Start: &proto.HarnessStart{},
		},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	handler := &terminalMetadataHandler{}
	if err := harness.DrainStream(ctx, stream, "exec-1", handler); err != nil {
		t.Fatalf("DrainStream: %v", err)
	}
	if handler.completed {
		t.Fatal("OnComplete called instead of the metadata-aware completion capability")
	}
	if !bytes.Equal(handler.metadata, want) {
		t.Fatalf("metadata = %q, want %q", handler.metadata, want)
	}
}
