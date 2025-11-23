# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY config/ ./config/
COPY internal/ ./internal/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o model-manager ./cmd/server

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app
RUN mkdir -p /app/config

# Copy the binary from builder
COPY --from=builder /build/model-manager .
COPY config/gpu-profiles.json /app/config/gpu-profiles.json

# Create non-root user
RUN adduser -D -u 1000 appuser && chown -R appuser:appuser /app
USER appuser

EXPOSE 8080

CMD ["./model-manager"]
