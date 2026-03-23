package testagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/ax/agent"
	"github.com/google/ax/internal/historyutil"
	testagentpb "github.com/google/ax/internal/testagent/proto"
	"github.com/google/ax/proto"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/anypb"
)

func Agents() map[string]agent.Agent {
	return map[string]agent.Agent{
		"coding":            &CodingAgent{},
		"docker-build":      &DockerBuilderAgent{},
		"docker-mirror":     &DockerMirrorAgent{},
		"kubernetes-deploy": &KubernetesDeployAgent{},
	}
}

type CodingAgent struct{}

// Connect handles processing of input content with callback handler.
func (a *CodingAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	exec := NewExecutor(e, o)

	var history []*proto.Content
	{
		inputs := []*proto.Content{
			{
				Role: "user",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "Generate Cloud Run Python server code. Only show Python code, the output should be deployable as a server. We will deploy it to Kubernetes.",
					},
				},
			},
		}
		inputs = append(inputs, start.Contents...)
		outputs, err := exec.Exec(ctx, "code", &proto.AgentStart{
			AgentId: "gemini",
			Contents:  inputs,
		})
		if err != nil {
			return err
		}
		history = append(inputs, outputs...)
	}

	{
		outputs, err := exec.Exec(ctx, "docker", &proto.AgentStart{
			AgentId: "docker-build",
			Contents:  history,
		})
		if err != nil {
			return err
		}
		history = append(history, outputs...)
	}

	{
		config, err := anypb.New(&testagentpb.KubernetesDeployAgentConfig{
			Regions: []string{"us-central1"},
		})
		if err != nil {
			return err
		}
		outputs, err := exec.Exec(ctx, "deploy", &proto.AgentStart{
			AgentId: "kubernetes-deploy",
			Contents:  history,
			Config:  config,
		})
		if err != nil {
			return err
		}
		history = append(history, outputs...)
		// User may need to take control back to confirm
		// or after decline.
		if historyutil.WaitsForUser(history) {
			return nil
		}
	}

	{
		config, err := anypb.New(&testagentpb.KubernetesDeployAgentConfig{
			Regions: []string{"europe-north1", "asia-east1", "us-west2"},
		})
		if err != nil {
			return err
		}
		outputs, err := exec.Exec(ctx, "deploy-more", &proto.AgentStart{
			AgentId: "kubernetes-deploy",
			Contents:  history,
			Config:  config,
		})
		if err != nil {
			return err
		}

		history = append(history, outputs...)
		if historyutil.WaitsForUser(history) {
			return nil
		}
	}

	if err := o(&proto.AgentOutputs{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "One last step, a summary...\n\n",
				},
			},
		}},
	}); err != nil {
		return err
	}

	{
		history = append(history, &proto.Content{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "Summarize what we did, and list links to the final deployment endpoints. Give instructions how to revert the deployments if needed",
				},
			},
		})
		_, err := exec.Exec(ctx, "summarize", &proto.AgentStart{
			AgentId: "gemini",
			Contents:  history,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// HealthCheck checks if the agent is healthy.
func (a *CodingAgent) HealthCheck(ctx context.Context) error {
	return nil
}

// Close gracefully shuts down the agent.
func (a *CodingAgent) Close() error {
	return nil
}

var pendingRegions = make(map[string][]string) // not for production

type KubernetesDeployAgent struct{}

func (a *KubernetesDeployAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	exec := NewExecutor(e, o)

	approved, conf := historyutil.HasConfirmationAnswer(start.Contents)
	if conf != nil && pendingRegions[conf.Id] != nil {
		if !approved {
			return nil
		}

		regions := pendingRegions[conf.Id]
		if err := o(&proto.AgentOutputs{
			Contents: []*proto.Content{{
				Role: "assistant",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: fmt.Sprintf("\nPicked %v region(s) for deployment.\n", strings.Join(regions, ",")),
					},
				},
			}},
		}); err != nil {
			return err
		}

		for _, region := range regions {
			if region != "us-central1" {
				_, err := exec.Exec(ctx, "mirror-" + region, &proto.AgentStart{
					AgentId: "docker-mirror",
					Contents: []*proto.Content{
						{
							Role: "user",
							Content: &proto.Content_Text{
								Text: &proto.TextContent{
									Text: "Provide a mirror to the region if the image doesn't exist.",
								},
							},
						},
					},
				})
				if err != nil {
					return err
				}
			}
			if err := o(&proto.AgentOutputs{
				Contents: []*proto.Content{{
					Role: "assistant",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: "* Deploying to " + region + ", this may take a while...\n",
						},
					},
				}},
			}); err != nil {
				return err
			}
			if err := o(&proto.AgentOutputs{
				Contents: []*proto.Content{{
					Role: "assistant",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: "* kubectl apply -f deployment.yaml\n",
						},
					},
				}},
			}); err != nil {
				return err
			}

			time.Sleep(1500 * time.Millisecond)
			if err := o(&proto.AgentOutputs{
				Contents: []*proto.Content{{
					Role: "assistant",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: fmt.Sprintf("* Deployment complete. You can access the service at https://%v.test.services.acme.com\n\n", region),
						},
					},
				}},
			}); err != nil {
				return err
			}
			delete(pendingRegions, conf.Id)
		}
		return nil
	}

	if start.Config == nil {
		return fmt.Errorf("no config for id=%v", execID)
	}
	var config testagentpb.KubernetesDeployAgentConfig
	if err := start.Config.UnmarshalTo(&config); err != nil {
		return err
	}
	if len(config.Regions) == 0 {
		return fmt.Errorf("no regions specified")
	}

	confID := uuid.NewString()
	pendingRegions[confID] = config.Regions
	return o(&proto.AgentOutputs{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Confirmation{
				Confirmation: &proto.ConfirmationContent{
					Id:       confID,
					Question: fmt.Sprintf("Picked %v region(s) to deploy, continue?", strings.Join(config.Regions, ",")),
				},
			},
		}},
	})
}

func (a *KubernetesDeployAgent) HealthCheck(ctx context.Context) error {
	return nil
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

func (e *Executor) Exec(ctx context.Context, execID string, start *proto.AgentStart) ([]*proto.Content, error) {
	var outputs []*proto.Content
	if execID == "" {
		var err error
		execID, err = randomHex(8)
		if err != nil {
			return nil, err
		}
	}
	if err := e.exec.Exec(ctx, execID, start, func(resp *proto.AgentOutputs) error {
		outputs = append(outputs, resp.Contents...)
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
