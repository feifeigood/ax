"""
GAR Agent Framework for Python

A simple framework for building Python agents that work with the GAR orchestrator.
"""

import grpc
from concurrent import futures
import time
from typing import Callable, Iterator, Optional, Dict
from google.protobuf import timestamp_pb2


class Agent:
    """
    Agent provides a simple framework for building Python agents.

    Usage:
        def process(session_id, inputs):
            for content in inputs:
                yield Content(role="assistant", type="text",
                             mimetype="text/plain", data=f"Processed: {content.data}")

        agent = Agent(agent_id="my-agent", process_func=process)
        agent.serve(port=50051)
    """

    def __init__(
        self,
        agent_id: str,
        process_func: Callable,
        lifecycle_func: Optional[Callable] = None,
        health_check_func: Optional[Callable] = None
    ):
        """
        Initialize an agent.

        Args:
            agent_id: Unique identifier for this agent
            process_func: Function that takes (session_id: str, inputs: list) and yields Content responses
            lifecycle_func: Optional function that yields LifecycleEvent objects
            health_check_func: Optional function that returns (healthy: bool, message: str, metadata: dict)
        """
        self.agent_id = agent_id
        self.process_func = process_func
        self.lifecycle_func = lifecycle_func
        self.health_check_func = health_check_func

    def _create_servicer(self, pb2, pb2_grpc):
        """Create the gRPC servicer implementation."""
        agent = self

        class AgentServicer(pb2_grpc.AgentServiceServicer):
            def Process(self, request_iterator, context):
                # Extract session_id from gRPC metadata
                metadata = dict(context.invocation_metadata())
                session_id = metadata.get('session-id', '')

                # Collect all content into a list
                inputs = list(request_iterator)

                # Process the list of content with session_id
                for response in agent.process_func(session_id, inputs):
                    if response:
                        yield response

            def StreamLifecycle(self, request_iterator, context):
                if agent.lifecycle_func:
                    for event in agent.lifecycle_func(context):
                        yield event
                else:
                    # Default: send periodic heartbeats
                    while context.is_active():
                        timestamp = timestamp_pb2.Timestamp()
                        timestamp.GetCurrentTime()
                        event = pb2.LifecycleEvent(
                            event_type=pb2.EVENT_TYPE_HEARTBEAT,
                            timestamp=timestamp
                        )
                        yield event
                        time.sleep(30)

            def HealthCheck(self, request, context):
                if agent.health_check_func:
                    healthy, message, metadata = agent.health_check_func()
                else:
                    healthy, message, metadata = True, "Agent is healthy", {}

                return pb2.HealthCheckResponse(
                    healthy=healthy,
                    message=message,
                    metadata=metadata or {}
                )

        return AgentServicer()

    def serve(self, port: int = 50051, max_workers: int = 10):
        """
        Start the gRPC server.

        Args:
            port: Port to listen on (default: 50051)
            max_workers: Maximum number of worker threads (default: 10)
        """
        # Import proto files (assuming they've been generated)
        try:
            import proto.gar_pb2 as pb2
            import proto.gar_pb2_grpc as pb2_grpc
        except ImportError:
            raise ImportError(
                "Proto files not found. Generate them first:\n"
                "  python -m grpc_tools.protoc -I. --python_out=. --grpc_python_out=. proto/gar.proto"
            )

        server = grpc.server(futures.ThreadPoolExecutor(max_workers=max_workers))
        pb2_grpc.add_AgentServiceServicer_to_server(
            self._create_servicer(pb2, pb2_grpc),
            server
        )
        server.add_insecure_port(f'[::]:{port}')
        server.start()
        print(f"Agent '{self.agent_id}' listening on port {port}")

        try:
            server.wait_for_termination()
        except KeyboardInterrupt:
            print("\nShutting down agent...")
            server.stop(grace=5)


# Convenience function for quick agent creation
def create_agent(
    agent_id: str,
    process_func: Callable,
    lifecycle_func: Optional[Callable] = None,
    health_check_func: Optional[Callable] = None,
    port: int = 50051
):
    """
    Create and start an agent in one call.

    Args:
        agent_id: Unique identifier for this agent
        process_func: Function that takes (session_id: str, inputs: list) and yields Content responses
        lifecycle_func: Optional function that yields LifecycleEvent objects
        health_check_func: Optional function that returns (healthy: bool, message: str, metadata: dict)
        port: Port to listen on (default: 50051)
    """
    agent = Agent(agent_id, process_func, lifecycle_func, health_check_func)
    agent.serve(port=port)
