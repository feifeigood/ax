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

import sys
import os
from concurrent import futures
import grpc
import asyncio
import importlib.util
import uuid

# Add the root of the project to sys.path to find generated protos
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../proto')))

import ax_pb2
import ax_pb2_grpc
import content_pb2
from google.adk.agents import Agent, InvocationContext
from google.adk.sessions import Session, InMemorySessionService
from google.genai import types
from google.adk.runners import Runner, Event

class ADKAgentServicer(ax_pb2_grpc.AgentServiceServicer):
    def __init__(self, agent, debug=False):
        self.agent = agent
        self.debug = debug
        self.session_service = InMemorySessionService()
        self.runner = Runner(session_service=self.session_service, agent=self.agent, app_name=self.agent.name)

    async def Connect(self, request_iterator, context):
        try:
            async for request in request_iterator:
                if self.debug:
                    print(f"\n{'='*80}\n[DEBUG] Incoming Request (conversation_id: {request.conversation_id}, exec_id: {request.exec_id})\n{'='*80}\n{request}\n{'-'*80}")
                
                # Use UUID as session id to ensure calls to ADK agents are stateless and isolated.
                session = await self.session_service.create_session(app_name=self.agent.name, user_id="AX_USER", session_id=str(uuid.uuid4()))
                conversation_id = request.conversation_id
                exec_id = request.exec_id
                if not request.HasField('start'):
                    raise ValueError(f"Expect start message, got: {request}")

                if not request.start.messages:
                    raise ValueError(f"Empty messages in start request")
                messages = request.start.messages
                
                # Separate historical messages from the latest active query.
                # Only execute/reply to the last message.
                historical_messages = messages[:-1]
                latest_message = messages[-1]
                
                # Hydrate past history into the session without executing them
                for msg in historical_messages:
                    # TODO - Conversion logic for different content types beyond text
                    content = types.Content(role=msg.role, parts=[types.Part(text=msg.content.text.text)])
                    event = Event(
                        invocation_id=str(uuid.uuid4()),
                        author=self.agent.name,
                        content=content
                    )
                    await self.session_service.append_event(session, event)

                # Execute only the latest query
                content = types.Content(role=latest_message.role, parts=[types.Part(text=latest_message.content.text.text)])
                async for event in self.runner.run_async(session_id=session.id, user_id=session.user_id, new_message=content):
                    if event.content and event.content.parts:
                        for part in event.content.parts:
                            # TODO - Conversion logic for different content types beyond text
                            if getattr(part, 'text', None):
                                if part.thought:
                                    thought_summary = content_pb2.ThoughtSummaryContent(
                                        text=content_pb2.TextContent(text=part.text)
                                    )
                                    response_msg = ax_pb2.Message(
                                        role="assistant",
                                        content=content_pb2.Content(
                                            thought=content_pb2.ThoughtContent(
                                                summary=[thought_summary]
                                            )
                                        )
                                    )
                                else:
                                    response_msg = ax_pb2.Message(
                                        role="assistant",
                                        content=content_pb2.Content(
                                            text=content_pb2.TextContent(text=part.text)
                                        )
                                    )
                                # Send conversion_id and exec_id as it is, since ADK agent is stateless.
                                yield_msg = ax_pb2.AgentMessage(
                                    conversation_id=conversation_id,
                                    exec_id=exec_id,
                                    outputs=ax_pb2.AgentOutputs(messages=[response_msg])
                                )
                                if self.debug:
                                    print(f"[DEBUG] Yielding AgentMessage Response Chunk:\n{yield_msg}\n{'-'*80}")
                                yield yield_msg

                # Clean up/delete the stateless session to free resources
                await self.session_service.delete_session(
                    app_name=self.agent.name,
                    user_id=session.user_id,
                    session_id=session.id
                )

                # Send End
                end_msg = ax_pb2.AgentMessage(
                    conversation_id=conversation_id,
                    exec_id=exec_id,
                    end=ax_pb2.AgentEnd()
                )
                if self.debug:
                    print(f"[DEBUG] Sending End AgentMessage:\n{end_msg}\n{'='*80}\n")
                yield end_msg
        except Exception as e:
            import logging
            logging.exception(f"CRITICAL ERROR in Connect: {e}")
            raise e

    async def HealthCheck(self, request, context):
        return ax_pb2.HealthCheckResponse(healthy=True, message="ADK Agent Wrapper is healthy")

class ADKAgentServer:
    def __init__(self, agent_file, port="50051", debug=False):
        self.agent_file = agent_file
        self.port = port
        self.debug = debug
        self.server = None
        self.agent = None

    def load_agent(self):
        print(f"Loading agent from {self.agent_file}...")
        spec = importlib.util.spec_from_file_location("agent_module", self.agent_file)
        if spec is None or spec.loader is None:
            raise FileNotFoundError(f"Could not find or load agent file: {self.agent_file}")
        agent_module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(agent_module)
        
        self.agent = getattr(agent_module, "root_agent", None)
        if not self.agent:
            raise ValueError(f"No root_agent found in {self.agent_file}")
        print("Agent loaded successfully.")

    async def start(self):
        if not self.agent:
            self.load_agent()
            
        self.server = grpc.aio.server()
        ax_pb2_grpc.add_AgentServiceServicer_to_server(ADKAgentServicer(self.agent, debug=self.debug), self.server)
        # TODO: Implement auth here and in other places in AX for a stable release.
        self.server.add_insecure_port(f'[::]:{self.port}')
        await self.server.start()
        print(f"Server started on port {self.port}")

    async def wait_for_termination(self):
        if self.server:
            await self.server.wait_for_termination()

    async def stop(self, grace=None):
        if self.server:
            await self.server.stop(grace)

async def main():
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--agent_file", default="examples/adk_agent/agent.py")
    parser.add_argument("--port", default="50051")
    parser.add_argument("--debug", action="store_true", help="Log all incoming and outgoing gRPC messages")
    args = parser.parse_args()

    server = ADKAgentServer(args.agent_file, args.port, debug=args.debug)
    await server.start()
    try:
        await server.wait_for_termination()
    except KeyboardInterrupt:
        print("Shutting down...")
        await server.stop()

if __name__ == '__main__':
    asyncio.run(main())
