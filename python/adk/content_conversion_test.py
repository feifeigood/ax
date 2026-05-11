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

import unittest
import sys
import os
from unittest.mock import MagicMock

from python.proto import content_pb2
from google.genai import types
from .content_conversion import ax_content_to_adk_part, adk_part_to_ax_content

class TestContentConversion(unittest.TestCase):

    def test_ax_content_to_adk_part_text(self):
        content = content_pb2.Content(text=content_pb2.TextContent(text="hello"))
        part = ax_content_to_adk_part(content)
        self.assertEqual(part.text, "hello")
        self.assertFalse(getattr(part, 'thought', False))

    def test_ax_content_to_adk_part_thought(self):
        thought_summary = content_pb2.ThoughtSummaryContent(
            text=content_pb2.TextContent(text="Thinking...")
        )
        content = content_pb2.Content(
            thought=content_pb2.ThoughtContent(
                summary=[thought_summary]
            )
        )
        part = ax_content_to_adk_part(content)
        self.assertEqual(part.text, "Thinking...")
        self.assertTrue(getattr(part, 'thought', False))

    def test_adk_part_to_ax_content_text(self):
        part = MagicMock()
        part.text = "hello"
        part.thought = False
        part.function_call = None
        content = adk_part_to_ax_content(part)
        self.assertEqual(content.text.text, "hello")

    def test_adk_part_to_ax_content_thought(self):
        part = MagicMock()
        part.text = "Thinking..."
        part.thought = True
        part.function_call = None
        content = adk_part_to_ax_content(part)
        self.assertTrue(content.HasField("thought"))
        self.assertEqual(len(content.thought.summary), 1)
        self.assertEqual(content.thought.summary[0].text.text, "Thinking...")

    def test_ax_content_to_adk_part_confirmation_request(self):
        content = content_pb2.Content(
            confirmation=content_pb2.ConfirmationContent(
                id="conf-123",
                question="Are you sure?"
            )
        )
        part = ax_content_to_adk_part(content)
        self.assertIsNotNone(part.function_call)
        self.assertEqual(part.function_call.name, "adk_request_confirmation")
        self.assertEqual(part.function_call.args["prompt"], "Are you sure?")
        self.assertEqual(part.function_call.id, "conf-123")
        self.assertNotIn("id", part.function_call.args)

    def test_ax_content_to_adk_part_confirmation_approval(self):
        content = content_pb2.Content(
            confirmation=content_pb2.ConfirmationContent(
                id="conf-123",
                approval=content_pb2.ApprovalDecision(approved=True)
            )
        )
        part = ax_content_to_adk_part(content)
        self.assertIsNotNone(part.function_response)
        self.assertEqual(part.function_response.name, "adk_request_confirmation")
        self.assertEqual(part.function_response.id, "conf-123")
        self.assertEqual(part.function_response.response["confirmed"], True)

    def test_ax_content_to_adk_part_confirmation_decline(self):
        content = content_pb2.Content(
            confirmation=content_pb2.ConfirmationContent(
                id="conf-123",
                decline=content_pb2.DeclineDecision(declined=True)
            )
        )
        part = ax_content_to_adk_part(content)
        self.assertIsNotNone(part.function_response)
        self.assertEqual(part.function_response.name, "adk_request_confirmation")
        self.assertEqual(part.function_response.id, "conf-123")
        self.assertEqual(part.function_response.response["confirmed"], False)

    def test_adk_part_to_ax_content_confirmation(self):
        class MockFunctionCall:
            def __init__(self, name, args, id):
                self.name = name
                self.args = args
                self.id = id

        part = MagicMock()
        part.text = None
        part.thought = False
        part.function_call = MockFunctionCall(
            name="adk_request_confirmation",
            args={"toolConfirmation": {"hint": "Are you sure?"}},
            id="conf-123"
        )
        content = adk_part_to_ax_content(part)
        self.assertTrue(content.HasField("confirmation"))
        self.assertEqual(content.confirmation.id, "conf-123")
        self.assertEqual(content.confirmation.question, "Are you sure?")

if __name__ == "__main__":
    unittest.main()
