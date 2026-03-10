.PHONY: all build proto test clean install

# Build all binaries
all: proto build

# Build binaries
build:
	@echo "Building gar..."
	@mkdir -p bin
	@go build -o bin/gar ./cmd/gar
	@echo "Building local agent example..."
	@go build -o bin/local_agent ./examples/local_agent
	@echo "Building remote agent example..."
	@go build -o bin/remote_agent ./examples/remote_agent
	@echo "Build complete!"

# Generate protobuf code
proto:
	@echo "Generating protobuf code..."
	@export PATH=$$PATH:$$(go env GOPATH)/bin && \
		protoc --go_out=. --go_opt=paths=source_relative \
		       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
		       proto/gar.proto
	@echo "Protobuf generation complete!"

# Run tests
test:
	@echo "Running tests..."
	@go test -v ./...

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf bin/
	@rm -rf eventlog/
	@echo "Clean complete!"

# Install gar to GOPATH/bin
install:
	@echo "Installing gar..."
	@go install ./cmd/gar
	@echo "Install complete!"

# Run local agent example
run-local:
	@go run ./examples/local_agent

# Run remote agent example
run-remote:
	@go run ./examples/remote_agent

# Install dependencies
deps:
	@echo "Installing dependencies..."
	@go mod download
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	@echo "Dependencies installed!"

# Build Sandbox Router Static Binary
build-router:
	@echo "Building sandbox-router Linux binary..."
	@GOOS=linux CGO_ENABLED=0 GOARCH=amd64 go build -o sandbox-router ./cmd/sandbox-router
