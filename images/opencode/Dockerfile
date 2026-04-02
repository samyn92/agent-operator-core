# Airgap-friendly OpenCode container image
#
# This image bundles the OpenCode binary with pre-cached npm provider packages
# so it can run in environments without internet access.
#
# Build args:
#   OPENCODE_VERSION - OpenCode release tag (e.g., v1.3.13)
#
# The pre-cached node_modules are stored at /opt/opencode/node_modules/ in the
# image. The operator's init container copies them to the PVC before OpenCode
# starts. The operator also sets BUN_CONFIG_REGISTRY=http://localhost:1 so that
# bun's runtime `bun install` fails fast instead of hanging when the npm
# registry is unreachable. OpenCode continues normally after the failure.
#
# The resulting image is suitable for use with the agent-operator on managed
# platforms that enforce image registry restrictions and read-only root
# filesystems.

# Stage 1: Download OpenCode binary
FROM alpine:3.21 AS downloader

ARG OPENCODE_VERSION=v1.3.13
ARG TARGETARCH

# Map Docker TARGETARCH to OpenCode release naming:
#   amd64 → x64-baseline-musl
#   arm64 → arm64-musl
RUN apk add --no-cache curl tar \
    && if [ "$TARGETARCH" = "arm64" ]; then \
         ARCH_SUFFIX="arm64-musl"; \
       else \
         ARCH_SUFFIX="x64-baseline-musl"; \
       fi \
    && curl -fsSL "https://github.com/anomalyco/opencode/releases/download/${OPENCODE_VERSION}/opencode-linux-${ARCH_SUFFIX}.tar.gz" \
       -o /tmp/opencode.tar.gz \
    && tar -xzf /tmp/opencode.tar.gz -C /tmp \
    && mv /tmp/opencode /usr/local/bin/opencode \
    && chmod +x /usr/local/bin/opencode \
    && rm -rf /tmp/opencode*

# Stage 2: Pre-cache npm packages that OpenCode installs at runtime
FROM oven/bun:1.3-alpine AS deps

# OpenCode runs `bun install` on first API call in two directories:
#   1. Config dir (~/.config/opencode) - installs @opencode-ai/plugin
#   2. Workspace dir (.opencode/) - also installs @opencode-ai/plugin
# Custom providers also need @ai-sdk/openai-compatible.
#
# We pre-install all required packages here. Only node_modules is copied to
# the final image — no lockfile or package.json, since OpenCode writes its
# own package.json at runtime with pinned versions. The operator sets
# BUN_CONFIG_REGISTRY=http://localhost:1 so bun fails fast instead of
# hanging when it can't resolve from the registry.
WORKDIR /cache
RUN echo '{"dependencies":{"@ai-sdk/openai-compatible":"latest","@opencode-ai/plugin":"latest"}}' > package.json \
    && (bun install --frozen-lockfile 2>/dev/null || bun install)

# Stage 3: Runtime
FROM alpine:3.21

# OpenCode embeds Bun which requires libgcc and libstdc++.
# ripgrep is used by OpenCode's grep/search tool.
RUN apk add --no-cache libgcc libstdc++ ripgrep git \
    && adduser -D -u 1000 -h /data opencode

# Disable Bun transpiler cache (incompatible with read-only rootfs)
ENV BUN_RUNTIME_TRANSPILER_CACHE_PATH=0

# Copy OpenCode binary
COPY --from=downloader /usr/local/bin/opencode /usr/local/bin/opencode

# Pre-cache npm packages at a path that won't be hidden by the PVC
# mount at /data. The operator's init container copies these to the PVC.
COPY --from=deps /cache/node_modules/ /opt/opencode/node_modules/

USER 1000:1000
# Use /data as WORKDIR, NOT /data/workspace. On Kubernetes, when a PVC is
# mounted at /data, the CRI pre-creates the WORKDIR directory as root with
# mode 0755. This makes /data/workspace unwritable by UID 1000 even with
# fsGroup. The operator sets WorkingDir: /data/workspace on the main container
# spec, and the init script creates /data/workspace as UID 1000.
WORKDIR /data

ENTRYPOINT ["opencode"]
