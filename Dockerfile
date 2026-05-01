FROM golang:1.25-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o moodle-mcp ./cmd/moodle-mcp/

# Final stage
FROM alpine:latest

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/moodle-mcp .

# Default port (overridable via PORT env on most cloud platforms)
ENV PORT=8080
EXPOSE 8080

# ENTRYPOINT pins the binary; CMD is the default args (rest mode for backward
# compatibility with existing users). Override CMD at runtime to switch modes:
#   docker run image -mode http
ENTRYPOINT ["./moodle-mcp"]
CMD ["-mode", "rest", "-port", "8080"]
