#!/bin/bash
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

# Exit immediately if a command exits with a non-zero status
set -e

# Function to cleanup background processes
cleanup() {
    echo "Cleaning up background processes..."
    jobs -p | xargs -I {} kill {} 2>/dev/null || true
}
trap cleanup EXIT

echo "Cleaning up port 50052 if in use..."
kill -9 $(lsof -t -i:50052) 2>/dev/null || true

echo "Starting DockerAgent in background..."
go run examples/docker_agent/main.go &
AGENT_PID=$!

echo "Starting AX Server in background..."
ax serve &
SERVER_PID=$!

# Give servers a few seconds to start up
echo "Waiting for servers to be ready..."
for i in {1..30}; do
    if (echo > /dev/tcp/localhost/50052) >/dev/null 2>&1 && (echo > /dev/tcp/localhost/8494) >/dev/null 2>&1; then
        echo "Servers are ready!"
        break
    fi
    echo "Waiting..."
    sleep 1
done

echo "Registering DockerAgent..."
ax register \
    --agent-id DockerAgent \
    --agent-addr localhost:50052 \
    --agent-name "Docker Agent" \
    --agent-description "Agent specialized in generating Dockerfiles. Call this agent whenever the user request involves creating, writing, or generating a Dockerfile."

echo "Executing task..."
ax exec --server localhost:8494 --input "Write a Dockerfile for a Python flask app that says hello world."

# Wait for the exec command to finish
# The trap will kill the background servers when the script exits
