# Agent eXecutor (AX)

> [!WARNING]
> 🚧 This project is in active development and may introduce breaking changes.

AX, short for Agent eXecutor, is a distributed agent runtime. It provides a
runtime that coordinates agentic loops, manages executions with event logging,
and communicates with both local and remote agents via streaming protocols.
AX knows how to resume and recover from failures, even in distributed fashion.

## Features

- **Distributed Runtime**: Controller, skills, tools, and agents can execute in isolation
- **Resumption**: Automatic recovery from failures or interruptions
- **Local & Remote Agents**: Support for both in-process and remote agent deployment
- **Tools and Skills**: Built-in bash tool and agent skills support
- **Registry**: Agent discovery and selection

Built-in consistency and resumability features:
- **Single-Writer Architecture**: Centralized controller ensures consistent state management
- **Event Log**: Durable execution state with automatic recovery

## Overview

```
┌────────────────────────┐
│      [Controller]      │                 ┌──────────────┐
│  - Executor            │--(in process)---| local  agent |
│  - Event Log           │                 └──────────────┘
│  - Registry            │                 ┌──────────────┐
│  - Local Agents        │--(gRPC stream)--| remote agent |
│  - Tools & Skills      │                 └──────────────┘
└────────────────────────┘
```

As agents evolve from simple assistants to autonomous workers,
developers need a robust runtime to manage state, ensure reliability,
and audit execution. As we are moving away from monolithic agents towards
distributed harnesses where tools, skills and agents are deployed as
isolated actors, a distributed runtime with dynamically spawned isolated
workers becomes a necessity. AX provides the foundational layer to fill these gaps.

While compute-agnostic, AX is optimized to provide the best
experience on Kubernetes.

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

# Using exec with an AX server
ax exec --input "Can you list me this directory?" --server localhost:8494
```

Conversations can be continued any time:

```bash
ax exec \
  --conversation d85a4b4e-c53b-4c84-b879-f10d905bce40 \
  --input "Show me the contents of README.md"
```

You can continue from a previous sequence if client got disconnected
during a conversation. In this example, we skip seeing the events up to 
sequence number 12:

```bash
ax exec \
  --conversation d85a4b4e-c53b-4c84-b879-f10d905bce40 \
  --last-seq 12 \
  --resume
```

Instead of running the default planning step, you can start executing
from any registered agent:

```bash
ax exec \
  --agent coding \
  --input "Can you write me a simple HTTP server in Python?"
```

If anything goes wrong during the execution of an agent,
you can resume an incomplete execution in a conversation:
```bash
ax exec \
  --conversation edf98ef5-4bb1-4a9e-a091-3a77e03727e6 \
  --agent "coding" \
  --resume
```

### 2. Run exec with Custom Agents

Most developers want to build their own agents. AX allows running custom agents as remote
or sandbox agents. This example demonstrates how the AX server executes remote agents
through the `AgentService.Connect` RPC.

**Terminal 1** - Start the remote agent server:
```bash
go run examples/remote_agent/main.go
```
The remote agent runs as a gRPC server implementing `AgentService` on port `:50051`.

**Terminal 2** - Start the AX controller server:
```bash
# Ensure the agent is registered as a remote agent in ax.yaml.
cat ax.yaml
# ...
registry:
  remote_agents:
    - id: "lowercase"
      name: "Lowercase Agent"
      description: "Converts text to lowercase."
      address: "localhost:50051"

ax serve
```
The server exposes the `AXService` on port `:8494` by default.

**Terminal 3** - Register the remote agent and execute:
```bash
ax exec \
    --server localhost:8494 \
    --input "HELLO, CAN YOU LOWERCASE WHAT I JUST SAID?"
```

## Usage

The `ax` command provides several subcommands:

### Execute

```bash
ax exec \
    --input <text> \
    [--conversation <id>] \
    [--agent <id>] \
    [--server <address>] \
    [--config <file>]
```

Executes a new agentic execution or automatically resumes an existing one. If the conversation ID already exists, the execution will be resumed from its last state with the new input (if any).

Options:
- `--input`: Input message to send to agents (required)
- `--conversation`: Conversation ID (optional, generates UUID if not provided, or resumes if exists)
- `--agent`: Agent ID to use (optional, defaults to planner)
- `--server`: gRPC controller server address (optional. If not provided, runs with a built-in server)
- `--config`: Path to YAML configuration file (only used with a built-in server, default: "ax.yaml")

**Examples:**

```bash
# Execute a new execution
ax exec --input "Hello agents!"

# Resume an existing execution with new input
ax exec --conversation a53d4db3-1165-4925-87da-be6c72bbdeb1 --input "Ok, now let's do something else..."

# Execute using server mode
ax exec --server localhost:8494 --input "Hello agents!"

# Execute using a custom agent
ax exec --agent coding --input "Hello coding agent, write me a cool Go program!"
```

### Fork

Fork an existing agentic event log from a specific checkpoint (or the latest state)
into a new event log.

```bash
ax eventlog fork \
    --src-conversation <id> \
    [--src-seq <number>] \
    [--dest-conversation <id>] \
    [--server <address>]
```

Options:
- `--src-conversation`: Source conversation ID to fork from (required)
- `--src-seq`: Sequence number to fork from (optional, defaults to latest)
- `--dest-conversation`: Destination conversation ID (optional, generates UUID if not provided)
- `--server`: gRPC controller server address (default: "localhost:8494")

**Example:**

```bash
# Fork from the latest state
ax eventlog fork --src-conversation 38460323-9a78-41cb-8991-022b0ff2c19c

# Fork from a specific checkpoint
ax eventlog fork --src-conversation 38460323-9a78-41cb-8991-022b0ff2c19c --src-seq 12

# Fork from a specific checkpoint to a new event log with a specific new ID
ax eventlog fork --src-conversation 38460323-9a78-41cb-8991-022b0ff2c19c --src-seq 12 --dest-conversation e5e26e38-53a2-4f22-b1cb-ae867357df83
```

### Trace

Visualize the trace of an agentic execution in a Web UI, directly fetching from the SQLite event log.

```bash
ax eventlog trace --conversation <id> [--addr <address>] [--config <file>]
```

This will parse the execution logs and spin up a local web server (defaulting to e.g. `http://localhost:8080`), automatically opening it in your browser.

Options:
- `--addr`: Server address to listen on (optional, defaults to "localhost:8080")
- `--config`: Path to YAML configuration file (optional, defaults to "ax.yaml")

**Examples:**

```bash
# Trace on default server localhost:8080
ax eventlog trace --conversation 1a6e0b29-87c2-4af0-81ac-0c73bf8fa293

# Trace on a custom server address and port
ax eventlog trace --conversation 1a6e0b29-87c2-4af0-81ac-0c73bf8fa293 --addr 0.0.0.0:9090
```

### Register

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

#### Serve

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
  sqlite:
    filename: "eventlog/log.sqlite"

planner:
  gemini:
    model: "gemini-3-flash-preview"
    timeout: "60s"
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
      name: "Uppercase Agent"
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

## Event Log Format

Event logs use `ConversationEvent` and `ExecutionEvent` messages available in the proto file
to keep a log of conversations and executions.

## Gemini Agent

AX includes a built-in Gemini agent that can be used to generate text based on a given prompt. The agent is registered as `gemini` and can be triggered as a standalone agent or used from custom agent implementations.

```bash
ax exec --agent gemini \
  --input "Hello, how are you?"
```

#### Authentication

The Gemini agent supports authentication using either Google AI Studio or Vertex AI:

```bash
# AI Studio API key based authentication.
export GEMINI_API_KEY="your-api-key"

# Vertex AI based authentication, ensure application
# default credentials are set up, gcloud auth application-default login.
export GCLOUD_PROJECT="your-project-id"
export GCLOUD_LOCATION="us-central1"
export GOOGLE_GENAI_USE_VERTEXAI=True
```

## Extensions

### Skills

AX includes built-in support for Agent Skills. See [Skills](docs/skills.md) for more.

### Bash Tool

The built-in planner is equipped with a `bash` tool that enables
it to execute general-purpose shell commands. The tool automatically
adapts to the user's operating system.

For safety and control, any execution initiated by the bash tool
requires explicit user approval via a confirmation flow before running.

### Custom Agents

There are several ways to register custom agents in AX by implementing
the `AgentService` interface defined in `proto/ax.proto`:

- [Remote Agent](docs/remote-agent.md)
- [Kubernetes Sandbox Agents](docs/k8s-sandbox-agent.md)

## What AX is NOT?
* An agentic framework, AX is agnostic of the framework used to build agents.
* A container or job scheduler, we delegate it to the new capabilities in Kubernetes.
* A specific harness like a coding agent, we allow bringing any harness as an agent.

## License

Apache 2.0
