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

// Package agent provides interfaces and implementations for local and remote agents.
// Agents process content through callback handlers and can be registered with the controller.
package agent

import (
	"context"

	"github.com/google/gar/proto"
)

// OutputHandler is a callback function that handles output content from an agent.
// It is called for each piece of content the agent generates.
type OutputHandler func(outgoing *proto.ProcessResponse) error

// Agent defines the common interface for both local and remote agents.
// Agents process content using callback handlers.
type Agent interface {
	// Process handles processing of input content.
	// It calls the output handler for each piece of content generated.
	// The handler may be called multiple times during processing.
	Process(ctx context.Context, sessionID string, incoming *proto.ProcessRequest, handler OutputHandler) error

	// HealthCheck checks if the agent is healthy and responsive.
	// Returns an error if the agent is unhealthy or unreachable.
	HealthCheck(ctx context.Context) error

	// Close gracefully shuts down the agent and releases resources.
	Close() error
}
