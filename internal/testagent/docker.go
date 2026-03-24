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

package testagent

import (
	"context"
	"time"

	"github.com/google/ax/agent"
	"github.com/google/ax/proto"
)

type DockerBuilderAgent struct{}

func (a *DockerBuilderAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	o(&proto.AgentOutputs{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "\n\nBuilding the docker image now...\n",
				},
			},
		}},
	})

	time.Sleep(500 * time.Millisecond)
	o(&proto.AgentOutputs{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "* us-central1-docker.pkg.dev/acme/test/test:latest is built and is ready to push.\n",
				},
			},
		}},
	})

	time.Sleep(1000 * time.Millisecond)
	o(&proto.AgentOutputs{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "* The container image is pushed.\n\n",
				},
			},
		}},
	})
	return nil
}

func (a *DockerBuilderAgent) HealthCheck(ctx context.Context) error {
	return nil
}

// Close gracefully shuts down the agent.
func (a *DockerBuilderAgent) Close() error {
	return nil
}

type DockerMirrorAgent struct{}

func (a *DockerMirrorAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	o(&proto.AgentOutputs{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "* Starting pushing docker image now...\n",
				},
			},
		}},
	})

	time.Sleep(2000 * time.Millisecond)
	o(&proto.AgentOutputs{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "* The container image is mirrored.\n",
				},
			},
		}},
	})
	return nil
}

func (a *DockerMirrorAgent) HealthCheck(ctx context.Context) error {
	return nil
}

// Close gracefully shuts down the agent.
func (a *DockerMirrorAgent) Close() error {
	return nil
}
