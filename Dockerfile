# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the operator
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o operator ./cmd/operator

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /app/operator .

USER 65532:65532

ENTRYPOINT ["/operator"]
