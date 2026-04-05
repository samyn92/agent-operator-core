# mcp-exec-server — Generic MCP stdio server for CLI command execution.
#
# This image provides a reusable MCP server that exposes a single "exec" tool
# for running CLI commands. It follows the same architecture as tool-bridge:
# the capability-gateway (injected via init container) spawns this binary as
# a subprocess and bridges its stdio to SSE/HTTP.
#
# Runtime flow:
#   1. Init container copies capability-gateway binary into /gateway/
#   2. Main container runs capability-gateway in MCP mode
#   3. Gateway spawns /app/mcp-exec-server as subprocess
#   4. mcp-exec-server exposes an "exec" tool over MCP stdio
#   5. Gateway bridges MCP stdio -> SSE for OpenCode connectivity
#   6. Gateway enforces deny rules, rate limiting, audit logging
#
# To add CLI tools to this image, extend it:
#
#   FROM ghcr.io/samyn92/mcp-exec-server:latest
#   RUN apk add --no-cache kubectl helm
#
# Or use it as a base and install tools at build time.
#
# Build:
#   docker build -t ghcr.io/samyn92/mcp-exec-server:latest images/mcp-exec-server/
#
# Environment variables (set by the operator via MCPCapabilitySpec):
#   TOOL_NAME         - Name of the exec tool (default: "exec")
#   TOOL_DESCRIPTION  - Description shown to the LLM (auto-generated if empty)
#   COMMAND_PREFIX    - Required prefix for all commands (e.g., "kubectl")
#   WORKSPACE_PATH    - Working directory for command execution (default: /data/workspace)
#   SERVER_NAME       - MCP server name for identification (default: "mcp-exec-server")
#   SERVER_VERSION    - MCP server version (default: "0.1.0")
#   MAX_TIMEOUT       - Maximum command timeout in seconds (default: 300)
#   LOG_LEVEL         - Logging verbosity: debug, info, warn, error (default: "info")

# Stage 1: Build — compile Go binary
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o mcp-exec-server .

# Stage 2: Production — minimal runtime image
FROM alpine:3.22

# Install common CLI tools that exec-server consumers may need.
# Extend this image to add specific tools (kubectl, helm, etc.)
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy the compiled binary
COPY --from=builder /build/mcp-exec-server ./mcp-exec-server

# Create standard mount points
RUN mkdir -p /data/workspace /tmp \
    && adduser -D -u 1000 appuser \
    && chown -R appuser:appuser /data /tmp

USER 1000:1000

# The capability-gateway spawns this as a subprocess.
# It is NOT the container entrypoint — the gateway is.
# This CMD is here for standalone testing and documentation.
CMD ["/app/mcp-exec-server"]
