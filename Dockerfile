
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

# This Dockerfile builds the container image for the AX system.
# It builds the 'ax' binary which can be used as a server or CLI.

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

# Build the ax binary
RUN go build -tags ate -o /app/bin/ax ./cmd/ax

# Runtime stage
# TODO: consider other options instead of Alpine
FROM alpine:3.19

# Install certificates for secure gRPC and external calls
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/bin/ax /usr/local/bin/ax

RUN addgroup -S ax && adduser -S ax -G ax
USER ax

EXPOSE 8494
ENTRYPOINT ["/usr/local/bin/ax"]
CMD ["serve"]
