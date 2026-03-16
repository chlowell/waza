# Dockerfile for waza - AI agent skill evaluation CLI
# This provides a containerized environment for running waza in CI/CD pipelines

# Stage 1: Build web dashboard
FROM node:22-alpine AS web-builder

WORKDIR /build/web

COPY web/package.json web/package-lock.json ./
RUN npm ci --silent

COPY web/ ./
RUN npm run build

# Stage 2: Build Go binary
FROM golang:1.26 AS builder

WORKDIR /build

# Copy built web assets from web-builder stage
COPY --from=web-builder /build/web/dist/ web/dist/

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary with static linking
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-s -w' -o waza ./cmd/waza

# Verify the binary works
RUN ./waza --version

# Stage 3: Final image
FROM node:25-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    python3

RUN npm install -g @github/copilot

WORKDIR /workspace

COPY --from=builder /build/waza /usr/local/bin/waza

RUN waza --version

ENTRYPOINT ["waza"]
CMD ["--help"]
