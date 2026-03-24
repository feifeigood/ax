# Remote Agent

Remote agents run as gRPC servers implementing the `AgentService` interface defined in `proto/ax.proto`. The controller executes remote agents by calling their `Process` RPC with bidirectional streaming.

See `examples/remote_agent/main.go` for a complete implementation.

**Workflow:**
1. Remote agent starts as gRPC server on a port (e.g., :50051)
2. Start the server: `ax serve`
3. Register the agent: `ax register --agent-id my-agent --agent-name "My Agent" --agent-description "Agent description" --agent-addr localhost:50051`
4. When the server executes, it calls the agent's `Process` RPC
5. AX streams input content → Agent processes → Agent streams output back

See `examples/remote_agent/main.go` for a complete implementation.
