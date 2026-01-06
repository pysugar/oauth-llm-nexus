# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with version info
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
        -X github.com/pysugar/oauth-llm-nexus/internal/version.Version=${VERSION} \
        -X github.com/pysugar/oauth-llm-nexus/internal/version.Commit=${COMMIT} \
        -X github.com/pysugar/oauth-llm-nexus/internal/version.BuildTime=${BUILD_TIME}" \
    -o nexus ./cmd/nexus

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN adduser -D -u 1000 nexus
USER nexus

# Copy binary from builder
COPY --from=builder /app/nexus /app/nexus

# Copy default config (will be overridden by volume mount if provided)
COPY --from=builder /app/config/model_routes.yaml /app/config/model_routes.yaml

# Create data directory
RUN mkdir -p /app/data

# Environment variables
ENV PORT=8080 \
    HOST=0.0.0.0 \
    NEXUS_MODE=release

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:${PORT}/ || exit 1

# Run the application
ENTRYPOINT ["/app/nexus"]
