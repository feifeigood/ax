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

package harness

import (
	"context"
	"fmt"
	"io"

	"github.com/google/ax/proto"
)

// terminalMetadataHandler is an internal, optional completion capability. It
// keeps Handler unchanged while allowing the controller to retain opaque
// metadata from harnesses that provide it.
type terminalMetadataHandler interface {
	OnCompleteWithMetadata(ctx context.Context, execID string, metadata []byte) error
}

// failMetadataHandler is the failure counterpart to terminalMetadataHandler:
// it lets the controller retain opaque metadata (e.g. token usage) collected
// before a failed execution.
type failMetadataHandler interface {
	OnFailWithMetadata(ctx context.Context, execID string, metadata []byte, cause error) error
}

// DrainStream reads from the harness gRPC stream until io.EOF, dispatching messages
// to the handler, and returns the final execution status.
func DrainStream(ctx context.Context, stream proto.HarnessService_ConnectClient, execID string, handler Handler) error {
	var endState proto.State
	var endErr error
	var harnessMetadata []byte
	hasEnd := false

	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("gRPC harness streaming failure: %w", err)
		}

		switch payload := resp.Type.(type) {
		case *proto.HarnessResponse_Outputs:
			for _, outMsg := range payload.Outputs.Messages {
				if err := handler.OnMessage(ctx, execID, outMsg); err != nil {
					return fmt.Errorf("failed to dispatch streamed output: %w", err)
				}
			}
		case *proto.HarnessResponse_End:
			hasEnd = true
			endState = payload.End.GetState()
			harnessMetadata = payload.End.GetHarnessMetadata()
			if endState == proto.State_STATE_FAILED {
				if errDetail := payload.End.GetError(); errDetail != nil {
					endErr = fmt.Errorf("harness failed: [%d] %s", errDetail.GetCode(), errDetail.GetDescription())
				} else {
					endErr = fmt.Errorf("harness failed with no error details")
				}
			}
		}
	}

	if !hasEnd {
		return fmt.Errorf("harness stream ended without HarnessEnd frame")
	}
	if endState == proto.State_STATE_FAILED {
		if len(harnessMetadata) > 0 {
			if fh, ok := handler.(failMetadataHandler); ok {
				return fh.OnFailWithMetadata(ctx, execID, harnessMetadata, endErr)
			}
		}
		return endErr
	}
	if len(harnessMetadata) > 0 {
		if metadataHandler, ok := handler.(terminalMetadataHandler); ok {
			return metadataHandler.OnCompleteWithMetadata(ctx, execID, harnessMetadata)
		}
	}
	return handler.OnComplete(ctx, execID)
}
