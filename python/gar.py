# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""
GAR Agent Framework for Python

A simple framework for building Python agents that work with the GAR orchestrator.
"""

import grpc
from concurrent import futures
from typing import Callable, Iterator, Optional, Dict


class Agent:
    """
    Agent provides a simple framework for building Python agents.

    Usage:
        def process(execution_id, inputs):
            for content in inputs:
                yield Content(role="assistant", type="text",
                             mimetype="text/plain", data=f"Processed: {content.data}")

        agent = Agent(process_func=process)
        agent.serve(port=50051)
    """

    def __init__(
        self,
        process_func: Callable,
        health_check_func: Optional[Callable] = None
    ):
        """
        Initialize an agent.

        Args:
            process_func: Function that takes (execution_id: str, inputs: list) and yields Content responses
            health_check_func: Optional function that returns (healthy: bool, message: str, metadata: dict)
        """
        self.process_func = process_func
        self.health_check_func = health_check_func

    def _create_servicer(self, pb2, pb2_grpc):
        """Create the gRPC servicer implementation."""
        agent = self

        class AgentServicer(pb2_grpc.AgentServiceServicer):
            def Process(self, request_iterator, context):
                # Extract id from gRPC metadata
                metadata = dict(context.invocation_metadata())
                id = metadata.get('execution-id', '')

                # Collect all content into a list
                inputs = list(request_iterator)

                # Process the list of content with id
                for response in agent.process_func(id, inputs):
                    if response:
                        yield response

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
        print(f"Agent listening on port {port}")

        try:
            server.wait_for_termination()
        except KeyboardInterrupt:
            print("\nShutting down agent...")
            server.stop(grace=5)


# Convenience function for quick agent creation
def create_agent(
    process_func: Callable,
    health_check_func: Optional[Callable] = None,
    port: int = 50051
):
    """
    Create and start an agent in one call.

    Args:
        process_func: Function that takes (execution_id: str, inputs: list) and yields Content responses
        health_check_func: Optional function that returns (healthy: bool, message: str, metadata: dict)
        port: Port to listen on (default: 50051)
    """
    agent = Agent(process_func, health_check_func)
    agent.serve(port=port)
