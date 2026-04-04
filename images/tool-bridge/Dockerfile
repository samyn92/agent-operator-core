# Tool Bridge — MCP stdio server that bridges AgentTool[] packages to the MCP protocol.
#
# This image is used as the base for MCP server pods that serve OCI tool packages.
# The capability-gateway (injected via init container) spawns this process and
# bridges its stdio to SSE/HTTP for OpenCode agent connectivity.
#
# Runtime flow:
#   1. Init containers pull OCI tool packages via crane into /tools/<name>/
#   2. Init container copies capability-gateway binary into /gateway/
#   3. Main container runs capability-gateway in MCP mode
#   4. Gateway spawns "node /app/dist/tool-bridge.js" as subprocess
#   5. tool-bridge loads /tools/<name>/index.js and serves them over MCP stdio
#   6. Gateway bridges MCP stdio → SSE for OpenCode connectivity
#
# Build:
#   docker build -t ghcr.io/samyn92/tool-bridge:latest images/tool-bridge/
#
# Environment variables (set by the operator):
#   TOOLS_DIR        - Directory to scan for tool packages (default: /tools)
#   WORKSPACE        - Working directory for tool execution (default: /workspace)
#   SERVER_NAME      - MCP server name for identification (default: tool-bridge)
#   SERVER_VERSION   - MCP server version (default: 0.1.0)
#   LOG_LEVEL        - Logging verbosity: debug, info, warn, error (default: info)
#
# Volumes:
#   /tools/           - Tool packages pulled by init containers (read-only)
#   /workspace/       - Shared workspace for tool execution (read-write, PVC)
#   /gateway/         - Capability-gateway binary (read-only, init container)

# Stage 1: Build — compile TypeScript
FROM node:22-alpine AS builder

WORKDIR /build

# Copy package files first for layer caching
COPY package.json package-lock.json* ./

# Install all dependencies (including devDependencies for tsc)
RUN npm install

# Copy TypeScript source and config
COPY tsconfig.json ./
COPY src/ ./src/

# Compile TypeScript to JavaScript
RUN npx tsc

# Stage 2: Production — minimal runtime image
FROM node:22-alpine

# Install CLIs that tool packages may shell out to.
# - git: required by tools/git (execFile("git", ...))
# - curl: useful for debugging, minimal footprint
RUN apk add --no-cache git curl \
    && adduser -D -u 1000 -h /home/bridge bridge

WORKDIR /app

# Copy compiled JavaScript (no production deps needed — zero dependencies)
COPY --from=builder /build/dist/ ./dist/

# Create mount points:
# /tools   — OCI tool packages pulled by init containers
# /workspace — shared workspace for tool execution (may be PVC)
RUN mkdir -p /tools /workspace \
    && chown -R bridge:bridge /tools /workspace

USER 1000:1000

# The capability-gateway spawns this as a subprocess.
# It is NOT the container entrypoint — the gateway is.
# This CMD is here for standalone testing and documentation.
CMD ["node", "dist/tool-bridge.js"]
