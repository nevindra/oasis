FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Copy only Go source — excludes docs/, scripts/, CLAUDE.md, tests etc.
# Keeps Docker build cache valid when non-source files change.
COPY cmd/ix/ cmd/ix/
COPY internal/ixd/ internal/ixd/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ix ./cmd/ix

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates curl git jq ripgrep fd-find \
    python3 python3-pip python3-venv \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /ix /usr/local/bin/ix
WORKDIR /workspace
EXPOSE 8080
CMD ["ix"]
