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

#!/usr/bin/env python3
"""
Example Python agent using the GAR framework.

This demonstrates a simple agent that uppercases input text.
"""

from gar import Agent
import proto.gar_pb2 as pb2


def process(execution_id, inputs):
    """Process incoming content list and yield responses"""
    for content in inputs:
        yield pb2.Content(
            role="assistant",
            type="text",
            mimetype="text/plain",
            data=f"Python processed (execution {execution_id}): {content.data.upper()}"
        )


def health_check():
    """Health check function that always returns healthy"""
    return True, "OK", {}


if __name__ == "__main__":
    agent = Agent(
        process_func=process,
        health_check_func=health_check
    )
    agent.serve(port=50051)
