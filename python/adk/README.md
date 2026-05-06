# AX ADK Agent Server Wrapper

This directory contains a Python gRPC wrapper designed to expose any [Google ADK](https://github.com/google/adk) agent as an `AgentService` compatible with the **Agent eXecutor (AX)** runtime.

The wrapper uses `grpc.aio` to run a fully asynchronous, high-performance gRPC server and processes incoming start messages independently within isolated sessions.

---

## Setup Instructions

### 1. Create & Activate a Virtual Environment

It is recommended to use a Python virtual environment (`.venv`) to avoid package conflicts. From the root of the repository, run:

```bash
# Create the virtual environment
python3 -m venv .venv

# Activate the environment (Linux/macOS)
source .venv/bin/activate

# Activate the environment (Windows)
.venv\Scripts\activate
```

### 2. Install Dependencies

Install the required libraries (including `grpcio`, `google-genai`, and `google-adk`) using the provided requirements file:

```bash
pip install -r python/adk/requirements.txt
```

---

## Running the Server

The server loads a target agent file dynamically. By default, it is configured to load the example ADK weather agent at `examples/adk_agent/agent.py`.

### 1. Configure API Credentials

Since the example ADK agent utilizes Gemini models, you need to supply a Gemini API key if using the example ADK agent:

```bash
export GEMINI_API_KEY="your-gemini-api-key-here"
```

### 2. Start the Server

Run the server using your Python virtual environment:

```bash
python python/adk/adk_agent_server.py [options]
```

#### Available CLI Options:
- `--agent_file`: Path to the Python file containing your ADK `root_agent` (default: `examples/adk_agent/agent.py`)
- `--port`: Port number to bind the gRPC server to (default: `50051`)
- `--debug`: Enable debug mode to log all incoming and outgoing gRPC messages (default: `False`)

#### Examples:

```bash
# Start with default weather agent on port 50051
python python/adk/adk_agent_server.py

# Start with debug logging enabled
python python/adk/adk_agent_server.py --debug

# Start with a custom agent file on a custom port with debug mode
python python/adk/adk_agent_server.py --agent_file path/to/your_agent.py --port 50055 --debug
```

Once started, the server will print:
```
Loading agent from examples/adk_agent/agent.py...
Agent loaded successfully.
Server started on port 50051
```

---

## Integrating with AX

To use your running ADK agent in an AX conversation, ensure that the agent is registered in your `ax.yaml` configuration. Example:

```yaml
registry:
  remote_agents:
    - id: "weather-agent"
      name: "Weather agent"
      description: "Agent to answer questions about the weather in a city."
      address: "localhost:50051"
      metadata:
        version: "1.0"
```
