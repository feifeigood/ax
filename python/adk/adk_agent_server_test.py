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
How to run these tests:

From the root directory of the project, with the virtual environment activated:
  python3 -m unittest python/adk/adk_agent_server_test.py
"""


import unittest
from unittest.mock import MagicMock, AsyncMock, patch
import asyncio
import sys
import os

# Add proto path to sys.path
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../proto')))

import ax_pb2
import content_pb2
from google.genai import types
from google.adk.runners import Event

from python.adk.adk_agent_server import ADKAgentServicer, ADKAgentServer


class TestADKAgentServicer(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.mock_agent = MagicMock()
        self.mock_agent.name = "WeatherAgent"
        self.servicer = ADKAgentServicer(self.mock_agent, debug=False)
        
        # Mock Session Service
        self.servicer.session_service = AsyncMock()
        # Mock Runner
        self.servicer.runner = MagicMock()

    async def test_health_check(self):
        request = MagicMock()
        context = MagicMock()
        response = await self.servicer.HealthCheck(request, context)
        self.assertTrue(response.healthy)
        self.assertEqual(response.message, "ADK Agent Wrapper is healthy")

    async def test_connect_missing_start_field(self):
        # Message missing the 'start' field
        request = ax_pb2.AgentMessage(
            conversation_id="conv-123",
            exec_id="exec-456"
        )

        async def mock_iterator():
            yield request

        context = MagicMock()
        with self.assertRaises(ValueError) as ctx:
            async for _ in self.servicer.Connect(mock_iterator(), context):
                pass
        self.assertIn("Expect start message", str(ctx.exception))

    async def test_connect_empty_start_messages(self):
        # Message with 'start' field but empty messages list
        start_req = ax_pb2.AgentStart(messages=[])
        request = ax_pb2.AgentMessage(
            conversation_id="conv-123",
            exec_id="exec-456",
            start=start_req
        )

        async def mock_iterator():
            yield request

        context = MagicMock()
        with self.assertRaises(ValueError) as ctx:
            async for _ in self.servicer.Connect(mock_iterator(), context):
                pass
        self.assertIn("Empty messages in start request", str(ctx.exception))

    async def test_connect_successful_flow(self):
        # 1. Setup inputs: 1 history message, 1 active prompt message
        hist_msg = ax_pb2.Message(
            role="user",
            content=content_pb2.Content(text=content_pb2.TextContent(text="Hello history"))
        )
        active_msg = ax_pb2.Message(
            role="user",
            content=content_pb2.Content(text=content_pb2.TextContent(text="What's the weather?"))
        )
        start_req = ax_pb2.AgentStart(messages=[hist_msg, active_msg])
        request = ax_pb2.AgentMessage(
            conversation_id="conv-123",
            exec_id="exec-456",
            start=start_req
        )

        async def mock_iterator():
            yield request

        # 2. Mock Session creation
        mock_session = MagicMock()
        mock_session.id = "session-id-789"
        mock_session.user_id = "AX_USER"
        self.servicer.session_service.create_session.return_value = mock_session

        # 3. Mock Runner.run_async return sequence
        async def mock_run_async(session_id, user_id, new_message):
            # Yield event 1
            yield Event(
                invocation_id="inv-0",
                author="WeatherAgent",
                content=types.Content(role="model", parts=[types.Part(text="The weather is ")])
            )
            # Yield event 2
            yield Event(
                invocation_id="inv-1",
                author="WeatherAgent",
                content=types.Content(role="model", parts=[types.Part(text="sunny!")])
            )

        self.servicer.runner.run_async = mock_run_async

        # 4. Consume generator output
        responses = []
        context = MagicMock()
        async for response in self.servicer.Connect(mock_iterator(), context):
            responses.append(response)

        # 5. Assertions
        # Expect 3 response messages: Response Chunk 1, Response Chunk 2, and End Message
        self.assertEqual(len(responses), 3)

        # Verify Session Service calls
        self.servicer.session_service.create_session.assert_called_once()
        self.assertEqual(self.servicer.session_service.append_event.call_count, 1) # Hydrated 1 history message
        self.servicer.session_service.delete_session.assert_called_once_with(
            app_name="WeatherAgent",
            user_id="AX_USER",
            session_id="session-id-789"
        )
        
        # Verify first response chunk
        self.assertEqual(responses[0].conversation_id, "conv-123")
        self.assertEqual(responses[0].exec_id, "exec-456")
        self.assertEqual(responses[0].outputs.messages[0].content.text.text, "The weather is ")

        # Verify second response chunk
        self.assertEqual(responses[1].conversation_id, "conv-123")
        self.assertEqual(responses[1].exec_id, "exec-456")
        self.assertEqual(responses[1].outputs.messages[0].content.text.text, "sunny!")

        # Verify final End response message
        self.assertEqual(responses[2].conversation_id, "conv-123")
        self.assertEqual(responses[2].exec_id, "exec-456")
        self.assertTrue(responses[2].HasField("end"))

    async def test_connect_with_thought_content(self):
        # 1. Setup inputs
        active_msg = ax_pb2.Message(
            role="user",
            content=content_pb2.Content(text=content_pb2.TextContent(text="Show me your thoughts"))
        )
        start_req = ax_pb2.AgentStart(messages=[active_msg])
        request = ax_pb2.AgentMessage(
            conversation_id="conv-123",
            exec_id="exec-456",
            start=start_req
        )

        async def mock_iterator():
            yield request

        # 2. Mock Session creation
        mock_session = MagicMock()
        mock_session.id = "session-id-789"
        mock_session.user_id = "AX_USER"
        self.servicer.session_service.create_session.return_value = mock_session

        # 3. Mock Runner.run_async returning a thought part
        mock_part = MagicMock()
        mock_part.text = "Thinking..."
        mock_part.thought = True

        mock_content = MagicMock()
        mock_content.role = "model"
        mock_content.parts = [mock_part]

        mock_event = MagicMock()
        mock_event.invocation_id = "inv-0"
        mock_event.author = "WeatherAgent"
        mock_event.content = mock_content

        async def mock_run_async(session_id, user_id, new_message):
            yield mock_event

        self.servicer.runner.run_async = mock_run_async

        # 4. Consume generator output
        responses = []
        context = MagicMock()
        async for response in self.servicer.Connect(mock_iterator(), context):
            responses.append(response)

        # 5. Assertions
        # Expect 2 response messages: Thought response chunk, and End Message
        self.assertEqual(len(responses), 2)

        # Verify first response chunk contains ThoughtContent
        self.assertEqual(responses[0].conversation_id, "conv-123")
        self.assertEqual(responses[0].exec_id, "exec-456")
        
        message = responses[0].outputs.messages[0]
        self.assertEqual(message.role, "assistant")
        self.assertTrue(message.content.HasField("thought"))
        self.assertEqual(len(message.content.thought.summary), 1)
        self.assertEqual(message.content.thought.summary[0].text.text, "Thinking...")

        # Verify Session Service delete is called
        self.servicer.session_service.delete_session.assert_called_once_with(
            app_name="WeatherAgent",
            user_id="AX_USER",
            session_id="session-id-789"
        )


class TestADKAgentServer(unittest.TestCase):
    def setUp(self):
        self.server = ADKAgentServer(agent_file="dummy_path.py")

    @patch("importlib.util.spec_from_file_location")
    @patch("importlib.util.module_from_spec")
    def test_load_agent_missing_root_agent(self, mock_module_from_spec, mock_spec_from_file):
        # Configure mock module to lack root_agent attribute
        mock_module = MagicMock()
        del mock_module.root_agent
        mock_module_from_spec.return_value = mock_module

        with self.assertRaises(ValueError) as ctx:
            self.server.load_agent()
        self.assertIn("No root_agent found in", str(ctx.exception))

    @patch("importlib.util.spec_from_file_location")
    @patch("importlib.util.module_from_spec")
    def test_load_agent_success(self, mock_module_from_spec, mock_spec_from_file):
        # Configure mock module with a valid root_agent attribute
        mock_agent = MagicMock()
        mock_module = MagicMock()
        mock_module.root_agent = mock_agent
        mock_module_from_spec.return_value = mock_module

        self.server.load_agent()
        self.assertEqual(self.server.agent, mock_agent)


if __name__ == "__main__":
    unittest.main()
