# mcp-exec-k8s — MCP exec server with kubectl, helm, and flux CLI
#
# Extends the mcp-exec-server base with Kubernetes tooling for cluster
# inspection and management. Used as the server image for k8s-exec capabilities.
#
# Included CLIs:
#   - kubectl (via Alpine package, pinned to latest stable)
#   - helm (via Alpine package)
#   - flux (Flux CLI for GitOps operations)
#
# Build:
#   docker build -t ghcr.io/samyn92/mcp-exec-k8s:latest images/mcp-exec-k8s/
#
# Usage in a Capability CR:
#   spec:
#     mcp:
#       mode: server
#       command: ["/app/mcp-exec-server"]
#       environment:
#         TOOL_NAME: "k8s"
#         TOOL_DESCRIPTION: "Execute kubectl, helm, and flux commands"
#         COMMAND_PREFIX: ""
#       server:
#         image: ghcr.io/samyn92/mcp-exec-k8s:latest
#         serviceAccountName: k8s-readonly

# Stage 1: Download flux CLI (not available via apk)
FROM alpine:3.22 AS flux-downloader

RUN apk add --no-cache curl tar

ARG FLUX_VERSION=2.5.1
ARG TARGETARCH=amd64

RUN curl -sL "https://github.com/fluxcd/flux2/releases/download/v${FLUX_VERSION}/flux_${FLUX_VERSION}_linux_${TARGETARCH}.tar.gz" \
    | tar xz -C /usr/local/bin/ flux

# Stage 2: Production image — extend mcp-exec-server base
ARG BASE_TAG=latest
FROM ghcr.io/samyn92/mcp-exec-server:${BASE_TAG}

USER root

# Install kubectl and helm via Alpine packages
RUN apk add --no-cache \
    kubectl \
    helm \
    jq \
    && rm -rf /var/cache/apk/*

# Copy flux from downloader stage
COPY --from=flux-downloader /usr/local/bin/flux /usr/local/bin/flux

# Verify installations
RUN kubectl version --client --output=yaml 2>/dev/null | head -3 \
    && helm version --short 2>/dev/null \
    && flux --version 2>/dev/null

USER 1000:1000

# The capability-gateway spawns /app/mcp-exec-server as a subprocess.
# This CMD is here for standalone testing.
CMD ["/app/mcp-exec-server"]
