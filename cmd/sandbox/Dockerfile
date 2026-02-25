# Stage 1: Build the Go sandbox binary.
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /sandbox ./cmd/sandbox/

# Stage 2: Runtime image with Python, Node.js, and common packages.
FROM python:3.12-slim

# Build dependencies for native Python packages (matplotlib, pillow, scipy).
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libffi-dev \
    libjpeg-dev \
    libpng-dev \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 22 (for future JS runtime support).
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Pre-install common scientific Python packages.
RUN pip install --no-cache-dir \
    matplotlib \
    pandas \
    numpy \
    seaborn \
    pillow \
    scipy \
    requests

# Create sandbox workspace root.
RUN mkdir -p /var/sandbox

COPY --from=builder /sandbox /usr/local/bin/sandbox

EXPOSE 9000
ENTRYPOINT ["/usr/local/bin/sandbox"]
