FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /ix ./cmd/ix

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y \
    bash curl git jq ripgrep fd-find \
    python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /ix /usr/local/bin/ix
WORKDIR /workspace
EXPOSE 8080
CMD ["ix"]
