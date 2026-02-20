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
│      [Controller]      │                 ┌──────────────┐
│  - Session Manager     │--(in process)---| local  agent |
│  - Event Log           │                 └──────────────┘
│  - Loop Executor       │                 ┌──────────────┐
│  - Agent Registry      │--(gRPC stream)--| remote agent |
└────────────────────────┘                 └──────────────┘
```

As agents move from simple interactions to "autonomous workers," most developers
will need what GAR provides: a way to manage state, ensure reliability, and audit
the process through a structured event log. It is a "runtime" in the same way 
Kubernetes is a runtime for containers. GAR provides the plumbing so developers
can focus on the logic.

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

### 1. Run trigger

The CLI provides an easy way to trigger a session by using the
agents and built-in tools already linked into the GAR binary.

```bash
# Using default gar.yaml
gar trigger --input "Can you list me this directory?"

# Using a custom configuration
gar trigger --input "Can you list me this directory?" --config my-config.yaml
```

You can continue a session any time:

```bash
gar trigger --session session123 --input "Show me the contents of README.md"
```

### 2. Run Remote Agent with GAR Server

Most developers want to register their custom remote agents.

This example demonstrates how the GAR server triggers remote agents through the `AgentService.Process` RPC. You can run this in two ways:

This is the standard way to run GAR, separating the controller from the trigger client.

**Terminal 1** - Start the remote agent server:
```bash
go run examples/remote_agent/main.go
```
The remote agent runs as a gRPC server implementing `AgentService` on port `:50051`.

**Terminal 2** - Start the GAR controller server:
```bash
gar serve
```
The GAR server exposes the `GARService` on port `:8494`.

**Terminal 3** - Register the remote agent and trigger a session:
```bash
# Register the remote agent with gar
gar register \
    --server localhost:8494 \
    --agent-id remote-echo-agent \
    --agent-name "Echo Agent" \
    --agent-description "Echoes input in uppercase" \
    --agent-addr localhost:50051

# Trigger a session - once server address is specified, gar will coordinate the remote agent via Process RPC accordingly
gar trigger \
    --server localhost:8494 \
    --session session123 \
    --input "Hello, can you uppercase what I just said?"
```

## Usage

### GAR CLI

The `gar` command provides several subcommands:

#### Trigger a Session

```bash
gar trigger \
    --input <text> \
    [--session <id>] \
    [--server <address>] \
    [--config <file>]
```

Triggers a new agentic loop session or automatically resumes an existing one. If the session ID already exists, the session will be resumed from its last state with the new input.

Options:
- `--input`: Input message to send to agents (required)
- `--session`: Unique session identifier (optional, generates UUID if not provided, or resumes if exists)
- `--server`: gRPC controller server address (optional. If not provided, runs with a built-in GAR server)
- `--config`: Path to YAML configuration file (only used with a built-in GAR server, default: "gar.yaml")

**Examples:**

```bash
# Trigger a new session
gar trigger --input "Hello agent"

# Resume an existing session with new input
gar trigger --session abc123 --input "Continue processing"

# Trigger using server mode (connect to gar serve)
gar trigger --server localhost:8494 --input "Use remote agent"

```

#### Fork a Session

Fork an existing session from a specific checkpoint (or the latest state) into a new session.

```bash
gar fork \
    --src-session <id> \
    [--src-checkpoint <id>] \
    [--dest-session <id>] \
    [--server <address>]
```

Options:
- `--src-session`: Source Session ID to fork from (required)
- `--src-checkpoint`: Checkpoint ID to fork from (optional, defaults to latest)
- `--dest-session`: Destination Session ID (optional, generates UUID if not provided)
- `--server`: gRPC controller server address (default: "localhost:8494")

**Example:**

```bash
# Fork from the latest state
gar fork --src-session abc123

# Fork from a specific checkpoint
gar fork --src-session abc123 --src-checkpoint "550e..."

# Fork from a specific checkpoint to a new session with a specific new session id
gar fork --src-session abc123 --src-checkpoint "550e..." --dest-session new-session-id 
```

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
max_steps: 5

health_check:
  # Health check interval for agents
  interval: 30s

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

Checkpoints provide a mechanism to save and resume session state at specific points. Every content event can create a checkpoint with a unique UUID.

**Usage Examples:**

```bash
# Fork from a checkpoint to a new session
gar fork --src-session session123 \
  --src-checkpoint "550e8400-e29b-41d4-a716-446655440000" \
  --dest-session session456

# Resume from the forked session
gar trigger --session session456 \
  --input "Try different approach"
```

### Event Log Format

Event logs use the `Event` message available in the protobuf.

## Building Custom Agents

### Local Agent

See `examples/local_agent/main.go` for a complete implementation.

### Remote Agent

Remote agents run as gRPC servers implementing the `AgentService` interface defined in `proto/gar.proto`. The gar controller triggers remote agents by calling their `Process` RPC with bidirectional streaming.

See `examples/remote_agent/main.go` for a complete implementation.

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

See `examples/python_agent/agent.py` for a complete implementation.

**Register and use:**
```bash
# Start the Python agent
python agent.py

# Register with gar (in another terminal)
gar register \
  --server localhost:8494 \
  --agent-id "text-processing-agent" \
  --agent-name "Text Processing Agent" \
  --agent-description "An agent that processes text" \
  --agent-addr localhost:50051

# Trigger a session
gar trigger \
  --server localhost:8494 \
  --session session123 \
  --input "Hello, I heard that there is an agent that can help with processing this text!"
```

## License

Apache 2.0
