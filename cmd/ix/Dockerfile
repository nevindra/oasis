# Stage 1: Build the Go ix binary.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Copy only Go source — excludes docs/, scripts/, CLAUDE.md, tests etc.
# Keeps Docker build cache valid when non-source files change.
COPY cmd/ix/ cmd/ix/
COPY internal/ixd/ internal/ixd/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ix ./cmd/ix

# Stage 2: Single runtime image with Python, Node.js, Chrome, and document generation.
FROM ubuntu:24.04

# System tools + build deps for native Python packages.
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates curl git jq ripgrep fd-find unzip \
    python3 python3-pip python3-venv \
    # Native build deps for matplotlib, Pillow, cairosvg.
    gcc python3-dev libffi-dev libjpeg-dev libpng-dev libcairo2-dev pkg-config \
    # Chrome install deps.
    wget gnupg \
    && rm -rf /var/lib/apt/lists/*

# fnm + Node.js 25 + pnpm.
ENV FNM_DIR=/usr/local/fnm
ENV PATH="$FNM_DIR/aliases/default/bin:$PATH"
RUN curl -fsSL https://fnm.vercel.app/install | bash -s -- --install-dir "$FNM_DIR" --skip-shell \
    && "$FNM_DIR/fnm" install 25 \
    && "$FNM_DIR/fnm" default 25 \
    && npm install -g corepack \
    && corepack enable pnpm

ENV PNPM_HOME="/root/.local/share/pnpm"
ENV PATH="$PNPM_HOME:$PATH"

# Google Chrome (Ubuntu 24.04 snap chromium doesn't work in Docker).
RUN wget -q -O - https://dl.google.com/linux/linux_signing_key.pub | gpg --dearmor -o /usr/share/keyrings/google-chrome.gpg \
    && echo "deb [arch=amd64 signed-by=/usr/share/keyrings/google-chrome.gpg] http://dl.google.com/linux/chrome/deb/ stable main" > /etc/apt/sources.list.d/google-chrome.list \
    && apt-get update \
    && apt-get install -y google-chrome-stable --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*

ENV CHROME_BIN=/usr/bin/google-chrome-stable
ENV CHROME_PATH=/usr/bin/google-chrome-stable

# Pinchtab bridge binary (browser tool).
COPY --from=pinchtab/pinchtab:latest /usr/local/bin/pinchtab /usr/local/bin/pinchtab

# uv — fast Python package manager (5-10x faster than pip).
COPY --from=ghcr.io/astral-sh/uv:0.11.2 /uv /usr/local/bin/uv

# Python dependencies (scientific + document generation).
COPY requirements.txt /tmp/requirements.txt
RUN uv --no-cache pip install --system --break-system-packages -r /tmp/requirements.txt && \
    rm /tmp/requirements.txt

# Node.js global dependencies (document generation + diagrams).
RUN pnpm add -g \
    playwright-core \
    pptxgenjs \
    chartjs-node-canvas \
    @mermaid-js/mermaid-cli \
    sharp \
    papaparse

# oasis-render CLI + renderer scripts.
COPY renderers/ /opt/oasis/renderers/
COPY bin/oasis-render /usr/local/bin/oasis-render
RUN chmod +x /usr/local/bin/oasis-render

# ix binary.
COPY --from=build /ix /usr/local/bin/ix

# Run with: docker run --shm-size=2g (Chrome needs larger shared memory)
WORKDIR /workspace
EXPOSE 8080
CMD ["ix"]
