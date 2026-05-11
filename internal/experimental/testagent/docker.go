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
	"fmt"
	"time"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/proto"
)

type DockerBuilderAgent struct{}

func (a *DockerBuilderAgent) Connect(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	o(&proto.AgentOutputs{
		Messages: []*proto.Message{{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "Building the OCI image now...",
					},
				},
			},
		}},
	})

	time.Sleep(500 * time.Millisecond)
	o(&proto.AgentOutputs{
		Messages: []*proto.Message{{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "* gcr.io/acme-test-images/test:latest is built and is ready to push.",
					},
				},
			},
		}},
	})

	fakeBlobs := []struct {
		action string
		sha    string
	}{
		{"existing blob", "dd64bf2dd177757451a98fcdc999a339c35dee5d9872d8f4dc69c8f3c4dd0112"},
		{"existing blob", "ebddc55facdc6b1f7e0f30816a5fc7cc62f38abdf76c0a8b0a0ce52085754795"},
		{"existing blob", "fa8ae93e2b3a7478248483e942ff665efa7219c6cd72d7a03c775372076e98dc"},
		{"existing blob", "d6b1b89eccacc15c2420b2776d72c1dae334a00805ed9af54bf2f71e4d536f28"},
		{"existing blob", "b4e6f1bfce0a1fba2b5421041552f4a897aada9cd5680926580f9e2c6247a7ae"},
		{"existing blob", "b839dfae01f66e15c6a8b63520557ed315bdfe036342fa7a0c537259f10d7a9a"},
		{"existing blob", "7c12895b777bcaa8ccae0605b4de635b68fc32d60fa08f421dc3818bf55ee212"},
		{"existing blob", "c172f21841dff4c8cf45cde46589c1c2616cefe7e819965e92e6d3475c428aa0"},
		{"existing blob", "52630fc75a18675c530ed9eba5f55eca09b03e91bd5bc15307918bbc1a7e7296"},
		{"existing blob", "b4242723c53fe4e094eb78569a2c15b6aafb8eb42aa9c3c2666130654a316ae2"},
		{"existing blob", "2780920e5dbfbe103d03a583ed75345306e572ec5a48cb10361f046767d9f29a"},
		{"existing blob", "3214acf345c0cc6bbdb56b698a41ccdefc624a09d6beb0d38b5de0b2303ecaf4"},
		{"existing blob", "bdfd7f7e5bf6fc27e70b59101db21c3d8284d283884419dd5fe7020583bb79ca"},
		{"existing blob", "250c06f7c38e52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8542"},
		{"existing blob", "45a0c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8543"},
		{"existing blob", "35b0c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8544"},
		{"existing blob", "25c0c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8545"},
		{"existing blob", "15d0c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8546"},
		{"existing blob", "a5e0c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8547"},
		{"existing blob", "b5f0c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8548"},
		{"existing blob", "c510c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8549"},
		{"existing blob", "d520c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8540"},
		{"existing blob", "e530c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf8541"},
		{"existing blob", "f540c806973c52dc77e5c7586c3e40280dc7ff9bb9007c396e06d96736cf854a"},
		{"pushed blob", "a317e2b795be724afc43a3105750093ad079326ee725d64289cf348b195cf11a"},
		{"pushed blob", "65fb0778273d2c73bfd73e62557babca6989710cc76302b634487a84f69b33d4"},
	}

	for _, b := range fakeBlobs {
		time.Sleep(100 * time.Millisecond)
		o(&proto.AgentOutputs{
			Messages: []*proto.Message{{
				Role: "assistant",
				Content: &proto.Content{
					Type: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: fmt.Sprintf("%s %s: sha256:%s", time.Now().Format("2006/01/02 15:04:05"), b.action, b.sha),
						},
					},
				},
			}},
		})
	}

	time.Sleep(1000 * time.Millisecond)
	o(&proto.AgentOutputs{
		Messages: []*proto.Message{{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "* The container image is successfully pushed.",
					},
				},
			},
		}},
	})
	return nil
}

// Close gracefully shuts down the agent.
func (a *DockerBuilderAgent) Close() error {
	return nil
}

type DockerMirrorAgent struct{}

func (a *DockerMirrorAgent) Connect(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	o(&proto.AgentOutputs{
		Messages: []*proto.Message{{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "* Validating the container image now...",
					},
				},
			},
		}},
	})

	time.Sleep(2000 * time.Millisecond)
	o(&proto.AgentOutputs{
		Messages: []*proto.Message{{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "* The container image is mirrored",
					},
				},
			},
		}},
	})
	return nil
}

// Close gracefully shuts down the agent.
func (a *DockerMirrorAgent) Close() error {
	return nil
}
