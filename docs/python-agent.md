# Remote Python Agent

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
  --conversation d85a4b4e-c53b-4c84-b879-f10d905bce40 \
  --input "Hello, can you echo what I just said?"
```
