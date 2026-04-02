# Build stage — runs natively on the build host, cross-compiles for TARGETARCH
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETARCH

WORKDIR /app

# Copy go mod files
COPY go.mod ./
RUN go mod download

# Copy source code
COPY *.go ./

# Cross-compile for the target architecture
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -a -installsuffix cgo -o telegram-channel .

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /app/telegram-channel .

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/telegram-channel"]
