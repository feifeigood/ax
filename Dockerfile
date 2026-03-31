
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

# This Dockerfile builds the container image for the 'uppercase' Sandbox Agent.
# The resulting image is pushed to Artifact Registry and referenced by the 
# SandboxTemplate manifest to be executed dynamically via GKE Agent Sandbox.
#
# For more details on GKE Agent Sandbox container requirements, see:
# https://docs.cloud.google.com/kubernetes-engine/docs/how-to/agent-sandbox

# Build stage
# TODO: consider other options instead of Alpine
FROM golang:alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make build-base

WORKDIR /app

# Download dependencies first to cache them
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the uppercase agent
RUN go build -o /app/bin/uppercase ./examples/k8s_sandbox_agent

# Runtime stage
# TODO: consider other options instead of Alpine
FROM alpine:3.19

# Install certificates for secure gRPC and external calls
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy the built binary
COPY --from=builder /app/bin/uppercase /usr/local/bin/uppercase

# Create a non-root user matching expected Kubernetes security context best practices
RUN addgroup -S ax && adduser -S ax -G ax
USER ax

# Expose standard remote agent port
EXPOSE 8494

# Set default command
ENTRYPOINT ["/usr/local/bin/uppercase"]
