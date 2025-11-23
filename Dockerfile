# --- Stage 1: Build ---
FROM golang:1.23-alpine AS builder

# Install necessary C libraries for SQLite
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Copy dependency files first (for better caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the binary
# CGO_ENABLED=1 is required for go-sqlite3
RUN CGO_ENABLED=1 GOOS=linux go build -o scheduler main.go

# --- Stage 2: Run ---
FROM alpine:latest

WORKDIR /root/

# Install CA Certificates (needed for SendGrid HTTPS calls) and Timezone data
RUN apk --no-cache add ca-certificates tzdata

# Copy the binary from the builder stage
COPY --from=builder /app/scheduler .

# Expose the port
EXPOSE 8080

# Run the binary
CMD ["./scheduler"]