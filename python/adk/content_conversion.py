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

from google.genai import types
from python.proto import content_pb2

def ax_content_to_adk_part(content: content_pb2.Content) -> types.Part:
    """Converts a AX Content to ADK Part."""
    # TODO - Conversion logic for more content types
    active_type = content.WhichOneof('type')
    
    if active_type == 'text':
        if content.text.text:
            return types.Part(text=content.text.text)
            
    elif active_type == 'thought':
        if content.thought.summary:
            texts = []
            for s in content.thought.summary:
                if s.WhichOneof('type') == 'text':
                    texts.append(s.text.text)
            text = "".join(texts)
            if text:
                part = types.Part(text=text)
                part.thought = True
                return part
                
    elif active_type == 'confirmation':
        decision = content.confirmation.WhichOneof('decision')
        if decision == 'approval':
            return types.Part(
                function_response=types.FunctionResponse(
                    id=content.confirmation.id,
                    name="adk_request_confirmation",
                    response={"confirmed": True}
                )
            )
        elif decision == 'decline':
            return types.Part(
                function_response=types.FunctionResponse(
                    id=content.confirmation.id,
                    name="adk_request_confirmation",
                    response={"confirmed": False}
                )
            )
        else:
            return types.Part(
                function_call=types.FunctionCall(
                    id=content.confirmation.id,
                    name="adk_request_confirmation",
                    args={
                        "prompt": content.confirmation.question
                    }
                )
            )
            
    return None

def adk_part_to_ax_content(part: types.Part) -> content_pb2.Content:
    """Converts an ADK Part to a AX Content."""
    # TODO - Conversion logic for more content types
    if getattr(part, 'text', None):
        if getattr(part, 'thought', None):
            thought_summary = content_pb2.ThoughtSummaryContent(
                text=content_pb2.TextContent(text=part.text)
            )
            return content_pb2.Content(
                thought=content_pb2.ThoughtContent(
                    summary=[thought_summary]
                )
            )
        else:
            return content_pb2.Content(
                text=content_pb2.TextContent(text=part.text)
            )
    elif part.function_call and part.function_call.name == "adk_request_confirmation":
        args = part.function_call.args
        tool_conf = args.get("toolConfirmation")
        question = tool_conf.get("hint") if isinstance(tool_conf, dict) else None
        if question is None:
            question = str(args)
        return content_pb2.Content(
            confirmation=content_pb2.ConfirmationContent(
                id=part.function_call.id,
                question=question
            )
        )
    return None
