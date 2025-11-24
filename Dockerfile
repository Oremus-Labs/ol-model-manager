# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build binaries
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o bin/model-manager ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o bin/model-manager-worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o bin/model-manager-sync ./cmd/sync

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app
RUN mkdir -p /app/config

# Copy the binary from builder
COPY --from=builder /build/bin/model-manager .
COPY --from=builder /build/bin/model-manager-worker .
COPY --from=builder /build/bin/model-manager-sync .
COPY config/gpu-profiles.json /app/config/gpu-profiles.json

# Create non-root user
RUN adduser -D -u 1000 appuser && chown -R appuser:appuser /app
USER appuser

EXPOSE 8080

CMD ["./model-manager"]
