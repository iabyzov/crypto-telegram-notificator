# Use the official Go image as the build stage
FROM golang:1.24.0 as builder

# Set the working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server .

# Use a minimal base image for the final stage
FROM gcr.io/distroless/base-debian11

# Copy the binary from builder
COPY --from=builder /app/server /server

# Expose port 8080
EXPOSE 8080

# Run the application
CMD ["/server"]
