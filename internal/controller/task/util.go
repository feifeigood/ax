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

package task

import (
	"context"

	"github.com/google/ax/agent"
	"github.com/google/ax/proto"
)

// agentFunc adapts a simple string→string function into the agent.Agent interface.
type agentFunc func(input []*proto.Content, tm agent.Executor, o agent.OutputHandler)

func (f agentFunc) Connect(ctx context.Context, execID string, start *proto.AgentStart, tm agent.Executor, o agent.OutputHandler) error {
	f(start.Contents, tm, o)
	return nil
}

func (f agentFunc) HealthCheck(_ context.Context) error { return nil }
func (f agentFunc) Close() error                        { return nil }

func AgentFunc(fn func(input []*proto.Content, tm agent.Executor, o agent.OutputHandler)) agent.Agent {
	return agentFunc(fn)
}

// text is a helper that builds a plain-text Content.
func text(role, s string) *proto.Content {
	return &proto.Content{
		Content: &proto.Content_Text{Text: &proto.TextContent{Text: s}},
		Role:    role,
	}
}
