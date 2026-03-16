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
	"github.com/google/ax/proto"
	"github.com/google/uuid"
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

// Process handles processing of input content with callback handler.
func (a *CodingAgent) Process(ctx context.Context, t *agent.Task, e agent.TaskExecutor, o agent.OutputHandler) error {
	exec := NewExecutor(e, o)
	{
		exec.Append(&proto.Content{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "Generate Cloud Run Python server code. Only show Python code, the output should be deployable as a server. We will deploy it to Kubernetes.",
				},
			},
		})
		exec.Append(t.Inputs...)
		outputs, err := exec.Exec(ctx, &Execution{
			TaskID:  "code",
			AgentID: "gemini",
			Inputs:  exec.History(),
		})
		if err != nil {
			return err
		}
		exec.Append(outputs...)
	}

	{
		outputs, err := exec.Exec(ctx, &Execution{
			TaskID:  "docker",
			AgentID: "docker-build",
			Inputs:  exec.History(),
		})
		if err != nil {
			return err
		}
		exec.Append(outputs...)
	}

	{
		exec.Append(&proto.Content{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "Deploy to us-central1.",
				},
			},
		})
		exec.Append(t.Inputs...)
		outputs, err := exec.Exec(ctx, &Execution{
			TaskID:  "deploy",
			AgentID: "kubernetes-deploy",
			Inputs:  exec.History(),
		})
		if err != nil {
			return err
		}
		exec.Append(outputs...)
		// User may need to take control back to confirm
		// or after decline.
		if historyutil.WaitsForUser(exec.History()) {
			return nil
		}
	}

	{
		exec.Append(&proto.Content{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "Can you deploy it other regions, avoiding the us-central1?",
				},
			},
		})
		exec.Append(t.Inputs...)
		outputs, err := exec.Exec(ctx, &Execution{
			TaskID:  "deploy-more",
			AgentID: "kubernetes-deploy",
			Inputs:  exec.History(),
		})
		if err != nil {
			return err
		}
		exec.Append(outputs...)
		if historyutil.WaitsForUser(exec.History()) {
			return nil
		}
	}

	if err := o(&proto.ProcessResponse{
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
		exec.Append(&proto.Content{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "Summarize what we did, and list links to the final deployment endpoints. Give instructions how to revert the deployments if needed",
				},
			},
		})
		_, err := exec.Exec(ctx, &Execution{
			TaskID:  "summarize",
			AgentID: "gemini",
			Inputs:  exec.History(),
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

func (a *KubernetesDeployAgent) Process(ctx context.Context, t *agent.Task, e agent.TaskExecutor, o agent.OutputHandler) error {
	exec := NewExecutor(e, o)

	approved, conf := historyutil.HasConfirmationAnswer(t.Inputs)
	if conf != nil && pendingRegions[conf.Id] != nil {
		if !approved {
			return nil
		}

		regions := pendingRegions[conf.Id]
		if err := o(&proto.ProcessResponse{
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
				_, err := exec.Exec(ctx, &Execution{
					TaskID:  "mirror-" + region,
					AgentID: "docker-mirror",
					Inputs: []*proto.Content{
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
			if err := o(&proto.ProcessResponse{
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
			if err := o(&proto.ProcessResponse{
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
			if err := o(&proto.ProcessResponse{
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

	var regions []string = []string{"europe-north1", "asia-east1", "us-west2"}
	{
		inputs := t.Inputs
		inputs = append(inputs, &proto.Content{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: "Return comma separated regions, no other text. If instructed to use a region already, just return it. Otherwise, return europe-north1,asia-east1,us-west2",
				},
			},
		})
		var outputs []*proto.Content
		if err := e.Exec(ctx, &agent.Task{
			ID:      "region-picker",
			AgentID: "gemini",
			Inputs:  inputs,
		}, func(resp *proto.ProcessResponse) error {
			outputs = append(outputs, resp.Contents...)
			return nil
		}); err != nil {
			return err
		}

		last := outputs[len(outputs)-1]
		text := last.GetContent().(*proto.Content_Text).Text.GetText()
		regions = strings.Split(text, ",")
	}

	confID := uuid.NewString()
	pendingRegions[confID] = regions
	return o(&proto.ProcessResponse{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Confirmation{
				Confirmation: &proto.ConfirmationContent{
					Id:       confID,
					Question: fmt.Sprintf("Picked %v region(s) to deploy, continue?", strings.Join(regions, ",")),
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
	exec    agent.TaskExecutor
	handler agent.OutputHandler
	history []*proto.Content
}

func NewExecutor(e agent.TaskExecutor, o agent.OutputHandler) *Executor {
	return &Executor{
		exec:    e,
		handler: o,
	}
}

type Execution struct {
	TaskID  string
	AgentID string
	Inputs  []*proto.Content
}

func (e *Executor) Exec(ctx context.Context, execution *Execution) ([]*proto.Content, error) {
	var outputs []*proto.Content
	if execution.TaskID == "" {
		var err error
		execution.TaskID, err = randomHex(8)
		if err != nil {
			return nil, err
		}
	}

	if err := e.exec.Exec(ctx, &agent.Task{
		ID:      execution.TaskID,
		AgentID: execution.AgentID,
		Inputs:  execution.Inputs,
	}, func(resp *proto.ProcessResponse) error {
		outputs = append(outputs, resp.Contents...)
		return e.handler(resp)
	}); err != nil {
		return nil, err
	}
	return outputs, nil
}

func (e *Executor) Append(c ...*proto.Content) {
	e.history = append(e.history, c...)
}

func (e *Executor) History() []*proto.Content {
	return e.history
}

func randomHex(n int) (string, error) {
	bytes := make([]byte, n/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
