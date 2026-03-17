# Agent eXecutor (AX)

🚧 This project is in active development and may introduce breaking changes to capabilities and APIs until the 1.0 release. 🚧

AX, a short for Agent eXecutor, is a single-writer agent orchestrator system built in Go. It provides a minimal runtime that coordinates agentic loops, manages executions with event logging, and communicates with both local and remote agents via streaming protocols.

## Features

- **Session Management**: Builtin event log management for starting, resuming, forking, and inspecting agentic loop executions
- **Local & Remote Agents**: Support for both in-process and remote agent deployment
- **Streaming**: gRPC bidirectional streaming for agent communication
- **Tools and Skills**: Built-in bash tool and agent skills support
- **Registry**: Agent discovery and automatic health monitoring

Built-in consistency and resumability features:
- **Single-Writer Architecture**: Centralized controller ensures consistent state management
- **Event Log**: Durable execution state with automatic recovery

## Overview

```
┌────────────────────────┐
│      [Controller]      │                 ┌──────────────┐
│  - Task Manager        │--(in process)---| local  agent |
│  - Event Log           │                 └──────────────┘
│  - Registry            │                 ┌──────────────┐
│  - Local Agents        │--(gRPC stream)--| remote agent |
│  - Tools & Skills      │                 └──────────────┘
└────────────────────────┘
```

As agents move from simple interactions to "autonomous workers," most developers
will need what AX provides: a way to manage state, ensure reliability, and audit
the process through a structured event log. It is a "runtime" for agents in the same way 
Kubernetes is a runtime for containers. AX is compute agnostic but aims to provide
the most comprehensive experience on Kubernetes.

AX provides the plumbing so developers can focus on building agentic applications instead
of rebuilding the same infrastructure over and over again.

## Installation

Install the ax CLI directly from the repository:

```bash
go install github.com/google/ax/cmd/ax@latest
```

### Verify Installation

Check that ax is installed correctly:

```bash
ax --help
```

You should see the ax CLI usage information.

## Quick Start

### 1. Run exec

The CLI provides an easy way to execute by using the
agents and built-in tools already linked into the AX binary.

```bash
# Using default ax.yaml
ax exec --input "Can you list me this directory?"

# Using a custom configuration
ax exec --input "Can you list me this directory?" --config my-config.yaml
```

You can continue an execution any time:

```bash
ax exec --id exec123 --input "Show me the contents of README.md"
```

Instead of running the default planner agent, you can run any registered agent:

```bash
ax exec --agent coding --input "Can you write me a simple HTTP server in Python?"
```

### 2. Run exec with Custom Agents

Most developers want to build their own agents. AX allows running custom agents as remote
or sandbox agents. This example demonstrates how the AX server executes remote agents
through the `AgentService.Process` RPC.

**Terminal 1** - Start the remote agent server:
```bash
go run examples/remote_agent/main.go
```
The remote agent runs as a gRPC server implementing `AgentService` on port `:50051`.

**Terminal 2** - Start the AX controller server:
```bash
ax serve
```
The server exposes the `AXService` on port `:8494`.

**Terminal 3** - Register the remote agent and execute:
```bash
ax register \
    --server localhost:8494 \
    --agent-id uppercase-agent \
    --agent-name "Uppercase Agent" \
    --agent-description "Converts input text to uppercase." \
    --agent-addr localhost:50051

ax exec \
    --server localhost:8494 \
    --id task123 \
    --input "Hello, can you uppercase what I just said?"
```

## Usage

### CLI

The `ax` command provides several subcommands:

#### Execute

```bash
ax exec \
    --input <text> \
    [--id <id>] \
    [--agent <id>] \
    [--server <address>] \
    [--config <file>]
```

Executes a new agentic execution or automatically resumes an existing one. If the ID already exists, the execution will be resumed from its last state with the new input (if any).

Options:
- `--input`: Input message to send to agents (required)
- `--id`: Unique identifier (optional, generates UUID if not provided, or resumes if exists)
- `--agent`: Agent ID to use (optional, defaults to planner)
- `--server`: gRPC controller server address (optional. If not provided, runs with a built-in server)
- `--config`: Path to YAML configuration file (only used with a built-in server, default: "ax.yaml")

**Examples:**

```bash
# Execute a new execution
ax exec --input "Hello agents!"

# Resume an existing execution with new input
ax exec --id abc123 --input "Ok, now let's do something else..."

# Execute using server mode
ax exec --server localhost:8494 --input "Hello agents!"

# Execute using a custom agent
ax exec --agent coding --input "Hello coding agent, write me a cool Go program!"
```

#### Fork an Event Log

Fork an existing agentic event log from a specific checkpoint (or the latest state)
into a new event log.

```bash
ax fork \
    --src-id <id> \
    [--src-checkpoint <id>] \
    [--dest-id <id>] \
    [--server <address>]
```

Options:
- `--src-id`: Source ID to fork from (required)
- `--src-checkpoint`: Checkpoint ID to fork from (optional, defaults to latest)
- `--dest-id`: Destination ID (optional, generates UUID if not provided)
- `--server`: gRPC controller server address (default: "localhost:8494")

**Example:**

```bash
# Fork from the latest state
ax fork --src-id abc123

# Fork from a specific checkpoint
ax fork --src-id abc123 --src-checkpoint "550e..."

# Fork from a specific checkpoint to a new event log with a specific new ID
ax fork --src-id abc123 --src-checkpoint "550e..." --dest-id new-id
```

#### Register a Remote Agent

```bash
ax register \
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
ax serve [--config <path>]
```

Starts the controller as a gRPC server using a YAML configuration file.

Options:
- `--config`: Path to YAML configuration file (default: "ax.yaml")

Example configuration file (`ax.yaml`):
```yaml
server:
  address: ":8494"

eventlog:
  db_filename: "eventlog/log.sqlite"

health_check:
  enabled: true
  interval: 30s

planner:
  gemini:
    model: "gemini-3-flash-preview"
    temperature: 0.7
    max_tokens: 8192
    timeout: 60s
    system_prompt: "..."
    skills_dir: "./examples/skills"

registry:
  remote_agents:
    - id: "remote-text-processor"
      name: "Remote Text Processor"
      description: "Converts text to lowercase."
      address: "localhost:50051"
      metadata:
       version: "1.0"
  k8s_sandbox_agents:
    - id: "uppercase"
      name: "Upper Case Agent"
      description: "Converts text to uppercase."
      sandbox_template_ref: "uppercase-agent-template"
      container_port: 8494
      use_router: true
      metadata:
       version: "1.0"

```

Example:
```bash
# Start server with default config (ax.yaml)
ax serve

# Start server with custom config
ax serve --config my-config.yaml
```

### Checkpoints

Checkpoints provide a mechanism to save and resume state at specific points.
Every content event can create a checkpoint with a unique UUID.

**Usage Examples:**

```bash
# Fork from a checkpoint to a new event log
ax fork --src-id task123 \
  --src-checkpoint "550e8400-e29b-41d4-a716-446655440000" \
  --dest-id task456

# Resume from the forked event log
ax exec --id task456 \
  --input "Try different approach"
```

### Event Log Format

Event logs use the `ExecutionEvent` message available in the protobuf.

## Built-in Capabilities

### Skills

AX includes built-in support for the agentskills.io discovery and execution protocol.

The planner automatically discovers skills from `~/.agents/skills` by default (or a custom directory specified in `ax.yaml`). These skills are provided to the planner as tools, allowing it to seamlessly read skill instructions and execute their scripts.

### Bash Tool

The built-in planner is equipped with a `bash` tool that enables it to execute general-purpose shell commands. The tool automatically adapts to the user's operating system.

For safety and control, any execution initiated by the bash tool requires explicit user approval via a confirmation flow before running.

### Gemini Agent

AX includes a built-in Gemini agent that can be used to generate text based on a given prompt. The agent is registered as `gemini`.

## Building Custom Agents

There are several ways to register custom agents in AX by implementing
the `AgentService` interface defined in `proto/ax.proto`:
- Remote agents
- GKE Sandbox agents

### Remote Agent

Remote agents run as gRPC servers implementing the `AgentService` interface defined in `proto/ax.proto`. The controller executes remote agents by calling their `Process` RPC with bidirectional streaming.

See `examples/remote_agent/main.go` for a complete implementation.

**Workflow:**
1. Remote agent starts as gRPC server on a port (e.g., :50051)
2. Start the server: `ax serve`
3. Register the agent: `ax register --agent-id my-agent --agent-name "My Agent" --agent-description "Agent description" --agent-addr localhost:50051`
4. When the server executes, it calls the agent's `Process` RPC
5. AX streams input content → Agent processes → Agent streams output back

See `examples/remote_agent/main.go` for a complete implementation.

### GKE Sandbox Agents

AX supports dynamically provisioning secure, isolated agents on Google Kubernetes Engine (GKE) via the [Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) feature. When an agent requires a Sandbox Agent, the AX server requests a temporary remote agent container in the cluster, establishes a secure connection locally (using port-forwarding via a proxy service), and cleans up the sandbox claim automatically upon closing.

#### Prerequisites
- A running Kubernetes cluster.
- The [Agent Sandbox Controller](https://github.com/kubernetes-sigs/agent-sandbox?tab=readme-ov-file#installation) installed.
- `kubectl` installed and configured locally.

#### Setup: Deploying the Router
Before using Sandbox Agents remotely and developing locally, you must deploy the `sandbox-router` into your cluster. This router proxies traffic securely to the isolated gVisor pods (direct port-forwarding to gVisor pods is not supported by Kubernetes due to netstack isolation).

1. Cross-compile the proxy binary via Make and inject it into an Alpine Pod:
```bash
# Build the router for linux and copy it into the cluster
make build-router
kubectl apply -f cmd/sandbox-router/sandbox-router.yaml
kubectl wait --for=condition=Ready pod -l app=sandbox-router --timeout=60s
POD_NAME=$(kubectl get pods -l app=sandbox-router -o jsonpath='{.items[0].metadata.name}')
kubectl cp sandbox-router $POD_NAME:/app/sandbox-router
kubectl exec $POD_NAME -- touch /app/.done
```

2. Expose it as a service:
```bash
kubectl expose deployment sandbox-router --port=8080 --target-port=8080
```

To use a Sandbox Agent, specify it in your `ax.yaml` configuration:

```yaml
registry:
  k8s_sandbox_agents:
    - id: "my-sandbox-agent"
      name: "Sandbox Worker"
      description: "An ephemeral sandbox processor"
      sandbox_template_ref: "your-gke-sandbox-template-name"
      container_port: 8494
```

#### End-to-End Example (Uppercase Agent)

AX provides a complete example of a Sandbox Agent in `examples/k8s_sandbox_agent/`. It receives text input via gRPC and returns the same text converted to uppercase.

You can test this agent end-to-end using the `ax` binary, which exercises the full `SandboxAgent` lifecycle (provisioning, port-forwarding, and remote execution).

**1. Build the Agent Image**

From the root of the AX repository:
```bash
docker build -t ax-uppercase:latest -f examples/sandbox_agent/Dockerfile .
```

**2. Publish Image to Registry**

When deploying to a cluster, you can host the agent container image in **any standard container registry** accessible by your Kubernetes cluster (e.g., Docker Hub, Google Artifact Registry, GitHub Container Registry).
- For production, update the `image` field in `examples/k8s_sandbox_agent/sandbox-template-and-pool.yaml` to your full registry path.

Once the image is available, register the SandboxTemplate:
```bash
kubectl apply -f examples/k8s_sandbox_agent/sandbox-template-and-pool.yaml
```

**3. Configure ax.yaml**
Ensure your `ax.yaml` references this sandbox agent:
```yaml
registry:
  k8s_sandbox_agents:
    - id: "uppercase"
      sandbox_template_ref: "uppercase-agent-template"
      container_port: 8494
      use_router: true
```

**4. Run the Server**
```bash
ax serve --config ax.yaml
```

**5. Run the Agent**
In a separate terminal:
```bash
ax exec --agent "uppercase" --input "hello world"
```

The system will dynamically create a `SandboxClaim`, establish a connection via `kubectl port-forward`, execute the code securely, and return the result.


#### Viewing Sandbox Logs
If you want to monitor the internal agent output or see if your gVisor sandbox received the physical gRPC requests:

1. List the active sandbox Pods:
```bash
kubectl get pods -n default
```

2. Fetch the logs for your specific sandbox claim:
```bash
kubectl logs -f ax-claim-uppercase-<hash_id>
```

### Remote Python Agent

AX is language agnostic and supports remote agents written in any language that can implement the `AgentService` interface. Python is a popular choice for AI agents, and AX provides first-class support for Python agents.

To build a Python agent, first set up your environment by installing dependencies and generating the gRPC artifacts from the protobuf definitions:

```bash
# Install dependencies
pip install grpcio grpcio-tools

# Generate Python code from proto file
python -m grpc_tools.protoc -I. --python_out=python --grpc_python_out=python proto/ax.proto
```

See `python/example_agent.py` for a complete implementation.

**Register and use:**
```bash
# Start the Python agent
python python/example_agent.py

# Register the agent (in another terminal)
ax register \
  --server localhost:8494 \
  --agent-id "echo-agent" \
  --agent-name "Echo Agent" \
  --agent-description "An agent that echoes the input." \
  --agent-addr localhost:50051

ax exec \
  --server localhost:8494 \
  --id task123 \
  --input "Hello, can you echo what I just said?"
```

## License

Apache 2.0
