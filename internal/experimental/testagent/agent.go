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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/historyutil"
	"github.com/google/ax/proto"
	"github.com/google/uuid"
)

// Please note that this is not production code. testagent is only for testing ax.
// and it will be deleted once we have comprehensive agents and integration tests.
// testagent is not meant to be used in production.

func Agents() map[string]agent.Agent {
	return map[string]agent.Agent{
		"coding":            &CodingAgent{},
		"docker-build":      &DockerBuilderAgent{},
		"docker-mirror":     &DockerMirrorAgent{},
		"kubernetes-deploy": &KubernetesDeployAgent{},
	}
}

func DockerBuild() agent.Agent {
	return &DockerBuilderAgent{}
}

func KubernetesDeploy() agent.Agent {
	return &KubernetesDeployAgent{}
}

type KubernetesDeployAgentConfig struct {
	Regions []string `json:"regions"`
}

type CodingAgent struct{}

// Connect handles processing of input content with callback handler.
func (a *CodingAgent) Connect(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	exec := NewExecutor(e, o)

	var history []*proto.Message
	{
		inputs := []*proto.Message{
			{
				Role: "user",
				Content: &proto.Content{
					Type: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: "Generate Cloud Run Python server code. Only show Python code, the output should be deployable as a server. We will deploy it to Kubernetes.",
						},
					},
				},
			},
		}
		inputs = append(inputs, start.Messages...)
		outputs, err := exec.Exec(ctx, conversationID, "code", &proto.AgentStart{
			AgentId:  "gemini",
			Messages: inputs,
		})
		if err != nil {
			return err
		}
		history = append(inputs, outputs...)
	}

	{
		outputs, err := exec.Exec(ctx, conversationID, "docker", &proto.AgentStart{
			AgentId:  "docker-build",
			Messages: history,
		})
		if err != nil {
			return err
		}
		history = append(history, outputs...)
	}

	{
		config, err := json.Marshal(&KubernetesDeployAgentConfig{
			Regions: []string{"us-central1"},
		})
		if err != nil {
			return err
		}
		outputs, err := exec.Exec(ctx, conversationID, "deploy", &proto.AgentStart{
			AgentId:     "kubernetes-deploy",
			Messages:    history,
			AgentConfig: config,
		})
		if err != nil {
			return err
		}
		history = append(history, outputs...)
		// User may need to take control back to confirm
		// or after decline.
		if historyutil.WaitsForUser(history) != nil {
			return nil
		}
	}

	{
		config, err := json.Marshal(&KubernetesDeployAgentConfig{
			Regions: []string{"europe-north1", "asia-east1", "us-west2"},
		})
		if err != nil {
			return err
		}
		outputs, err := exec.Exec(ctx, conversationID, "deploy-more", &proto.AgentStart{
			AgentId:     "kubernetes-deploy",
			Messages:    history,
			AgentConfig: config,
		})
		if err != nil {
			return err
		}

		history = append(history, outputs...)
		if historyutil.WaitsForUser(history) != nil {
			return nil
		}
	}

	if err := o(&proto.AgentOutputs{
		Messages: []*proto.Message{{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "One last step, a summary...",
					},
				},
			},
		}},
	}); err != nil {
		return err
	}

	{
		history = append(history, &proto.Message{
			Role: "user",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "Summarize what we did, and list links to the final deployment endpoints. Give instructions how to revert the deployments if needed",
					},
				},
			},
		})
		_, err := exec.Exec(ctx, conversationID, "summarize", &proto.AgentStart{
			AgentId:  "gemini",
			Messages: history,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Close gracefully shuts down the agent.
func (a *CodingAgent) Close() error {
	return nil
}

var pendingRegions = make(map[string][]string) // not for production

type KubernetesDeployAgent struct{}

func (a *KubernetesDeployAgent) Connect(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	exec := NewExecutor(e, o)

	approved, conf := historyutil.HasConfirmationAnswer(start.Messages)
	if conf != nil && pendingRegions[conf.Id] != nil {
		if !approved {
			return nil
		}

		regions := pendingRegions[conf.Id]
		var history []*proto.Message
		history = append(history, start.Messages...)

		out := func(text string) error {
			msg := &proto.Message{
				Role: "assistant",
				Content: &proto.Content{
					Type: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: text,
						},
					},
				},
			}
			history = append(history, msg)
			return o(&proto.AgentOutputs{
				Messages: []*proto.Message{msg},
			})
		}

		if err := out(fmt.Sprintf("Picked %v region(s) for deployment.", strings.Join(regions, ","))); err != nil {
			return err
		}

		for _, region := range regions {
			if err := out(fmt.Sprintf("* Setting context to gke_acme-production_%s_cluster-1...", region)); err != nil {
				return err
			}

			time.Sleep(500 * time.Millisecond)
			if err := out("* Running: kubectl apply -f deployment.yaml\nnamespace/test-acme unchanged\nconfigmap/test-config created\ndeployment.apps/test-service created\nservice/test-service created"); err != nil {
				return err
			}

			time.Sleep(1000 * time.Millisecond)

			for i := 0; i <= 3; i++ {
				msg := fmt.Sprintf("* Waiting for deployment \"test-service\" rollout to finish: %d of 3 updated replicas are available...", i)
				if i == 3 {
					msg = "* deployment \"test-service\" successfully rolled out"
				}
				if err := out(msg); err != nil {
					return err
				}
				time.Sleep(1000 * time.Millisecond)
			}

			if err := out(fmt.Sprintf("* Deployment complete. You can access the service at https://%v.test.services.acme.com", region)); err != nil {
				return err
			}
			delete(pendingRegions, conf.Id)
		}

		history = append(history, &proto.Message{
			Role: "user",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "Summarize in one paragraph what we did.",
					},
				},
			},
		})
		if _, err := exec.Exec(ctx, conversationID, "summarize", &proto.AgentStart{
			AgentId:  "gemini",
			Messages: history,
		}); err != nil {
			return err
		}

		return nil
	}

	var config KubernetesDeployAgentConfig
	if start.AgentConfig == nil {
		config.Regions = []string{"us-central1", "us-east1", "us-west1"}
	} else {
		if err := json.Unmarshal(start.AgentConfig, &config); err != nil {
			return err
		}
	}
	if len(config.Regions) == 0 {
		return fmt.Errorf("no regions specified")
	}

	confID := uuid.NewString()
	pendingRegions[confID] = config.Regions
	return o(&proto.AgentOutputs{
		Messages: []*proto.Message{{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Confirmation{
					Confirmation: &proto.ConfirmationContent{
						Id:       confID,
						Question: fmt.Sprintf("Picked %v region(s) to deploy, continue?", strings.Join(config.Regions, ", ")),
					},
				},
			},
		}},
	})
}

// Close gracefully shuts down the agent.
func (a *KubernetesDeployAgent) Close() error {
	return nil
}

type Executor struct {
	exec    agent.Executor
	handler agent.OutputHandler
}

func NewExecutor(e agent.Executor, o agent.OutputHandler) *Executor {
	return &Executor{
		exec:    e,
		handler: o,
	}
}

func (e *Executor) Exec(ctx context.Context, conversationID string, execID string, start *proto.AgentStart) ([]*proto.Message, error) {
	var outputs []*proto.Message
	if execID == "" {
		var err error
		execID, err = randomHex(8)
		if err != nil {
			return nil, err
		}
	}
	if _, err := e.exec.Exec(ctx, conversationID, execID, start, func(resp *proto.AgentOutputs) error {
		outputs = append(outputs, resp.Messages...)
		return e.handler(resp)
	}); err != nil {
		return nil, err
	}
	return outputs, nil
}

func randomHex(n int) (string, error) {
	bytes := make([]byte, n/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
