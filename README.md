# gar

GAR, a short for Google Agent Runtime, is a single-writer agent orchestrator system built in Go. It provides a minimal runtime that coordinates agentic loops, manages sessions with event logging, and communicates with both local and remote agents via streaming protocols.

## Features

- **Streaming**: gRPC bidirectional streaming for agent communication
- **Local & Remote Agents**: Support for both in-process and remote agent deployment
- **Session Management**: Start, pause, resume, and inspect agentic loop sessions
- **Agent Registry**: Automatic health monitoring and agent discovery

Built-in consistency and resumability features:
- **Single-Writer Architecture**: Centralized controller ensures consistent state management
- **Event Log**: Durable session state with automatic recovery

## Overview

```
┌────────────────────────┐
│      [Controller]      │
│  - Session Manager     │
│  - Event Log           │
│  - Loop Executor       │
│  - Agent Registry      │
└──────┬──────────┬──────┘
       │          │
  (in-process) (gRPC stream)
       │          │
   ┌───────┐  ┌───────┐
   │ Local │  │Remote │
   │ Agent │  │ Agent │
   └───────┘  └───────┘
```

## Installation

Install the gar CLI directly from the repository:

```bash
go install github.com/google/gar/cmd/gar@latest
```

### Verify Installation

Check that gar is installed correctly:

```bash
gar --help
```

You should see the gar CLI usage information.

## Quick Start

### 1. Run Local Agent Example

```bash
# Run the local agent example by linking the local agent with controller.
go run examples/local_agent/main.go
```

### 2. Run Remote Agent with GAR Server

This example demonstrates how the gar server triggers remote agents through the
AgentService.Process RPC.

**Terminal 1** - Start the remote agent server:
```bash
go run examples/remote_agent/main.go
```

The remote agent runs as a gRPC server implementing AgentService on port :50051.

**Terminal 2** - Start the gar controller server:
```bash
gar serve
```

The gar server exposes the GARService on port :8494.

**Terminal 3** - Register the remote agent and trigger a session:
```bash
# Register the remote agent with gar
gar register \
    --agent-id remote-echo-agent \
    --agent-name "Echo Agent" \
    --agent-description "Echoes input in uppercase" \
    --agent-addr localhost:50051

# Trigger a session - gar will trigger the remote agent via Process RPC
gar trigger \
    --session-id session123 \
    --input "Hello remote agent"

# Inspect session details
gar inspect --session-id session123
```

## Usage

### GAR CLI

The `gar` command provides several subcommands:

#### Trigger a Session

```bash
gar trigger \
    --input <text> \
    [--session-id <id>] \
    [--checkpoint <uuid>] \
    [--server <address>]
```

Triggers a new agentic loop session or automatically resumes an existing one. If the session ID already exists, the session will be resumed from its last checkpoint (or a specific checkpoint if provided) with the new input.

Options:
- `--input`: Input message to send to agents (required)
- `--session-id`: Unique session identifier (optional, generates UUID if not provided, or resumes if exists)
- `--checkpoint`: Resume from specific checkpoint (empty for latest)
- `--server`: gRPC controller server address (default: "localhost:8494")

**Examples:**

```bash
# Trigger a new session
gar trigger --input "Hello agent"

# Resume an existing session with new input
gar trigger --session-id abc123 --input "Continue processing"

# Resume from a specific checkpoint (useful for undoing mistakes or exploring alternatives)
gar trigger --session-id abc123 \
    --checkpoint "550e8400-e29b-41d4-a716-446655440000" \
    --input "Try a different approach"
```

#### Inspect a Session

```bash
gar inspect --session-id <id> [--server <address>]
```

Options:
- `--session-id`: Session identifier to inspect (required)
- `--server`: gRPC controller server address (default: "localhost:8494")

#### Register a Remote Agent

```bash
gar register \
    --agent-id <id> \
    --agent-addr <address> \
    --agent-name <name> \
    --agent-description <desc> \
    [--server <address>]
```

Options:
- `--agent-id`: Unique agent identifier (required)
- `--agent-addr`: gRPC agent server address (e.g., "localhost:50051") (required)
- `--agent-name`: Human-readable name for the agent (required)
- `--agent-description`: Description of agent capabilities (required)
- `--server`: gRPC controller server address (default: "localhost:8494")

#### Run Server

```bash
gar serve [--config <path>]
```

Starts the controller as a gRPC server using a YAML configuration file.

Options:
- `--config`: Path to YAML configuration file (default: "gar.yaml")

Example configuration file (`gar.yaml`):
```yaml
server:
  address: ":8494"

eventlog:
  dir: "eventlog"

# Maximum steps per trigger
max_steps: 50

# Health check interval for agents
health_check_interval: 30s

# Remote agents to register on startup
remote_agents:
  - id: "text-processing-agent"
    name: "Text Processing Agent"
    description: "An agent for text processing"
    address: "localhost:50051"
    metadata:
      version: "1.0"
```

Example:
```bash
# Start server with default config (gar.yaml)
gar serve

# Start server with custom config
gar serve --config my-config.yaml
```

### Checkpoints

Checkpoints provide a mechanism to save and resume session state at specific points. Every content event (both `CONTENT_IN` and `CONTENT_OUT`) automatically creates a checkpoint with a unique UUID.

**Usage Examples:**

```bash
# Inspect a session to see available checkpoints
gar inspect --session-id session123

# Resume from a specific checkpoint
gar trigger --session-id session123 \
  --checkpoint "550e8400-e29b-41d4-a716-446655440000" \
  --input "Try different approach"
```


### Event Log Format

Event logs use JSON Lines format (one JSON object per line). Each entry includes the session ID, a monotonic sequence number, and checkpoint ID (for content events) for traceability:

```json
{"session_id": "session123", "timestamp": "2026-01-02T10:30:00Z", "seq": 0, "type": "CONTENT_IN", "checkpoint_id": "550e8400-e29b-41d4-a716-446655440000", "data": {...}}
{"session_id": "session123", "timestamp": "2026-01-02T10:30:01Z", "seq": 1, "type": "CONTENT_OUT", "checkpoint_id": "6ba7b810-9dad-11d1-80b4-00c04fd430c8", "data": {...}}
{"session_id": "session123", "timestamp": "2026-01-02T10:30:02Z", "seq": 2, "type": "STATE", "data": {...}}
```

**Event Types:**
- `CONTENT_IN`: Incoming content from user or external source (includes `checkpoint_id`)
- `CONTENT_OUT`: Outgoing content from agents (includes `checkpoint_id`)
- `STATE`: Session state changes, e.g. running, completed, failed.

## Building Custom Agents

### Local Agent

```go
import (
    "context"
    "github.com/google/gar/agent"
    "github.com/google/gar/proto"
)

// Define your process function using callback handler
processFunc := func(ctx context.Context, sessionID string, inputs []*proto.Content, handler agent.OutputHandler) error {
    for _, content := range inputs {
        output := &proto.Content{
            Role:     "assistant",
            Type:     "text",
            Mimetype: "text/plain",
            Data:     "Your response: " + content.Data,
        }
        if err := handler(output); err != nil {
            return err
        }
    }
    return nil
}

// Define health check function (optional)
healthCheckFunc := func(ctx context.Context) error {
    // Return nil if healthy, error otherwise
    return nil
}

// Create the agent
myAgent, err := agent.NewLocalAgent(agent.LocalAgentConfig{
    ID:              "my-agent",
    ProcessFunc:     processFunc,
    HealthCheckFunc: healthCheckFunc,     // optional
})
```

### Remote Agent

Remote agents run as gRPC servers implementing the `AgentService` interface defined in `proto/gar.proto`. The gar controller triggers remote agents by calling their `Process` RPC with bidirectional streaming.

```go
type server struct {
    proto.UnimplementedAgentServiceServer
}

// Process handles bidirectional streaming - gar controller calls this RPC
func (s *server) Process(stream proto.AgentService_ProcessServer) error {
    for {
        // Receive input content from gar controller
        content, err := stream.Recv()
        if err == io.EOF {
            return nil
        }
        if err != nil {
            return err
        }

        // Process the content
        output := &proto.Content{
            Role:     "assistant",
            Type:     "text",
            Mimetype: "text/plain",
            Data:     "Processed: " + content.Data,
        }

        // Send response back to gar controller
        if err := stream.Send(output); err != nil {
            return err
        }
    }
}

func (s *server) HealthCheck(ctx context.Context, req *proto.HealthCheckRequest) (*proto.HealthCheckResponse, error) {
    // Return health status for gar controller health monitoring
    return &proto.HealthCheckResponse{
        Healthy: true,
        Message: "Agent is healthy",
    }, nil
}
```

**Workflow:**
1. Remote agent starts as gRPC server on a port (e.g., :50051)
2. Start gar controller: `gar serve`
3. Register with gar: `gar register --agent-id my-agent --agent-name "My Agent" --agent-description "Agent description" --agent-addr localhost:50051`
4. When gar triggers a session, it calls the agent's `Process` RPC
5. GAR streams input content → Agent processes → Agent streams output back

See `examples/remote_agent/main.go` for a complete implementation.

### Remote Python Agent

Python agents can be built using the GAR agent framework. First, install dependencies and generate Python gRPC code:

```bash
# Install dependencies
pip install grpcio grpcio-tools

# Generate Python code from proto file
python -m grpc_tools.protoc -I. --python_out=. --grpc_python_out=. proto/gar.proto
```

Then implement your agent using the framework:

```python
from gar import Agent
import proto.gar_pb2 as pb2

def process(session_id, inputs):
    """Process incoming content list and yield responses"""
    for content in inputs:
        yield pb2.Content(
            role="assistant",
            type="text",
            mimetype="text/plain",
            data=f"Python processed: {content.data.upper()}"
        )

def health_check():
    """Health check function that always returns healthy"""
    return True, "OK", {}

# Create and start the agent
agent = Agent(
    agent_id="python-agent",
    process_func=process,
    health_check_func=health_check
)
agent.serve(port=50051)
```

**Register and use:**
```bash
# Start the Python agent
python agent.py

# Register with gar (in another terminal)
gar register \
  --agent-id python-agent \
  --agent-name "Text Processing Agent" \
  --agent-description "An agent that processes text" \
  --agent-addr localhost:50051

# Trigger a session
gar trigger \
  --session-id session123 \
  --input "Hello, I heard that there is an agent that can help with processing this text!"
```

## Future Enhancements

- [ ] Observability and trajectory collection
- [ ] TLS support for remote agents
- [ ] Make checkpointing optional
- [ ] gar deploy from container
- [ ] Fork session when resuming from a checkpoint that isn't the latest
- [ ] Create a package of local agents (listing files, reading files, grep'ing the directory)
- [ ] Web UI

## License

Apache 2.0
