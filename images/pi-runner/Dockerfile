# Pi Runner — Lightweight container for running pi-agent-core agents as K8s Jobs
#
# The runner loads a user-provided agent TypeScript module (mounted at /agent/),
# executes it with a given prompt, and streams events as JSONL to stdout.
#
# The WorkflowRun controller creates Jobs using this image, mounting agent code
# via ConfigMap volumes (inline/configMapRef) or init containers (OCI).
#
# Build:
#   docker build -t ghcr.io/samyn92/pi-runner:latest images/pi-runner/
#
# Runtime env vars (set by the controller):
#   MODEL_PROVIDER    - e.g., "anthropic"
#   MODEL_NAME        - e.g., "claude-sonnet-4-20250514"
#   PROVIDER_API_KEY  - API key for the provider
#   THINKING_LEVEL    - off|minimal|low|medium|high|xhigh
#   TOOL_EXECUTION    - parallel|sequential
#   PROMPT            - The prompt to execute
#   TRIGGER_DATA      - JSON trigger context from the WorkflowRun
#
# Volumes:
#   /agent/index.js   - The compiled agent module (tools + config)
#   /output/          - Result output directory (optional, for ConfigMap pickup)

# Stage 1: Build — install deps and compile TypeScript
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

# Install git (agents may need it for repo operations).
# node:22-alpine already provides a "node" user with UID 1000.
RUN apk add --no-cache git

WORKDIR /app

# Copy only production dependencies
COPY package.json ./
RUN npm install --omit=dev && npm cache clean --force

# Copy compiled JavaScript from builder
COPY --from=builder /build/dist/ ./dist/

# Create mount points for agent code, output, and workspace
RUN mkdir -p /agent /output /workspace && chown -R node:node /agent /output /workspace

USER 1000:1000

# The agent module is mounted at /agent/index.js by the controller.
# The runner reads env vars for all configuration.
ENTRYPOINT ["node", "dist/runner.js"]
