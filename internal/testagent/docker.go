package testagent

import (
	"context"
	"time"

	"github.com/google/ax/agent"
	"github.com/google/ax/proto"
)

type DockerBuilderAgent struct{}

func (a *DockerBuilderAgent) Process(ctx context.Context, t *agent.Task, e agent.TaskExecutor, o agent.OutputHandler) error {
	o(&proto.ProcessResponse{
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
	o(&proto.ProcessResponse{
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
	o(&proto.ProcessResponse{
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

func (a *DockerMirrorAgent) Process(ctx context.Context, t *agent.Task, e agent.TaskExecutor, o agent.OutputHandler) error {
	o(&proto.ProcessResponse{
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
	o(&proto.ProcessResponse{
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
