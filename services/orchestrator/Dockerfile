# Stage 1: Build the application
FROM golang:1.21-alpine AS builder

# Set the working directory
WORKDIR /app

# Copy go.mod and go.sum files to download dependencies
COPY ../../go.mod ../../go.sum ./
COPY ../../go.work ../../go.work ./
COPY ../../go.work.sum ../../go.work.sum ./

# Copy internal libraries that are needed
COPY ../../internal ./internal
COPY ../../api ./api

# Download dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build the application
# CGO_ENABLED=0 is important for a static binary
# -o /app/orchestrator builds the binary into the /app folder with the name "orchestrator"
RUN CGO_ENABLED=0 go build -o /app/orchestrator .

# Stage 2: Create the final, lightweight image
FROM alpine:latest

# Set the working directory
WORKDIR /app

# Copy the built binary from the builder stage
COPY --from=builder /app/orchestrator .

# Expose the port for health checks
EXPOSE 8080

# Command to run the application
CMD ["./orchestrator"]
