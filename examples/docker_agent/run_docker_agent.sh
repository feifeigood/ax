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

echo "Waiting for agent to be ready..."
sleep 3

echo "Running test client..."
go run examples/docker_agent/test/test_docker_agent.go
