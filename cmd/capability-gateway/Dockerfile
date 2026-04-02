# Build stage for capability-gateway
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the capability-gateway binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o capability-gateway ./cmd/capability-gateway/main.go

# Runtime stage — minimal image for the gateway binary.
#
# Two use cases:
#   1. MCP server mode (init container): The init container copies
#      /capability-gateway into a shared emptyDir volume. The main container
#      (user's MCP server image) then runs it. Only needs the binary.
#
#   2. CLI sidecar mode: Runs as a sidecar in agent pods, providing
#      a REST /exec endpoint for Container capabilities (kubectl, gh, etc.).
#
# We use alpine + common tools to support both cases from a single image.
FROM alpine:3.21

RUN apk add --no-cache \
    ca-certificates \
    git \
    curl \
    jq

WORKDIR /

COPY --from=builder /app/capability-gateway /capability-gateway

# Create non-root user
RUN adduser -D -u 1000 gateway
USER gateway

# Health check (works for both modes — both expose /healthz)
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q --spider http://localhost:8080/healthz || exit 1

ENTRYPOINT ["/capability-gateway"]
