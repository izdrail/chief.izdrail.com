# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git gcc musl-dev

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
# We disable CGO for a static binary (no ALSA dependency)
RUN CGO_ENABLED=0 go build -o chief ./cmd/chief

# Final stage
FROM alpine:latest

RUN apk add --no-cache ca-certificates git

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/chief /usr/local/bin/chief

# Create the .chief directory structure
RUN mkdir -p .chief/prds

# Expose the server port
EXPOSE 1248

# Run the server by default
CMD ["chief", "serve"]
