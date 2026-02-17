# Deployment

This guide covers building, containerizing, and deploying Oasis to production.

## Local Build

```bash
go build ./cmd/oasis/
```

Produces a single `oasis` binary. CGO is not required for the basic build (uses `modernc.org/sqlite` which is pure Go), but CGO_ENABLED=1 gives better SQLite performance.

### With CGO (recommended for production)

```bash
CGO_ENABLED=1 go build -o oasis ./cmd/oasis/
```

Requires a C compiler (`gcc` or `musl-dev` on Alpine).

## Docker

The included `Dockerfile` produces a minimal Alpine-based image:

```dockerfile
FROM golang:1.24-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o oasis ./cmd/oasis/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/oasis /usr/local/bin/oasis
COPY oasis.toml /etc/oasis/oasis.toml
ENTRYPOINT ["oasis"]
```

### Build and Run

```bash
docker build -t oasis .
docker run --env-file .env oasis
```

### Image Size

The final image is lightweight:
- Builder stage: Go toolchain + dependencies
- Runtime stage: Alpine (~5MB) + binary (~10-15MB) + ca-certificates
- Total: ~20MB

## Environment Variables in Production

Pass secrets via environment variables, never in `oasis.toml`:

```bash
docker run \
  -e OASIS_TELEGRAM_TOKEN="..." \
  -e OASIS_LLM_API_KEY="..." \
  -e OASIS_EMBEDDING_API_KEY="..." \
  -e OASIS_BRAVE_API_KEY="..." \
  oasis
```

Or use `--env-file`:

```bash
docker run --env-file .env.production oasis
```

## Database Options

### Option 1: Local SQLite (default)

The database file is created inside the container at the path specified in `oasis.toml` (default: `oasis.db`).

**With persistent volume:**

```bash
docker run \
  -v oasis-data:/data \
  -e OASIS_CONFIG=/etc/oasis/oasis.toml \
  --env-file .env.production \
  oasis
```

With `oasis.toml` configured:

```toml
[database]
path = "/data/oasis.db"
```

### Option 2: Turso (remote libSQL)

For managed, persistent storage that survives container restarts without volumes:

```bash
# Create a Turso database
turso db create oasis

# Get connection details
turso db show oasis --url
turso db tokens create oasis
```

Set in environment:

```bash
OASIS_TURSO_URL="libsql://oasis-yourname.turso.io"
OASIS_TURSO_TOKEN="your-turso-auth-token"
```

Turso is recommended for cloud deployments where persistent volumes are expensive or unavailable.

## Cloud Deployment

### Zeabur

1. Connect your GitHub repository to Zeabur
2. Zeabur detects the `Dockerfile` and builds automatically
3. Set environment variables in the Zeabur dashboard:
   - `OASIS_TELEGRAM_TOKEN`
   - `OASIS_LLM_API_KEY`
   - `OASIS_EMBEDDING_API_KEY`
   - `OASIS_TURSO_URL` + `OASIS_TURSO_TOKEN` (recommended for persistence)
4. Deploy

### Railway / Fly.io / Render

Any platform that supports Dockerfiles works. The pattern is the same:

1. Build from `Dockerfile`
2. Set environment variables
3. No ports to expose (Oasis uses outbound-only long-polling, no inbound HTTP)

**No inbound ports needed.** Oasis connects to Telegram via long-polling (outbound HTTPS), so you don't need to configure webhooks, ingress, or SSL certificates.

### Health Monitoring

Oasis logs to stdout. Monitor for:
- `oasis: app running` -- successful startup
- `[recv]` lines -- incoming messages being processed
- `[auth] DENIED` -- unauthorized access attempts
- Error lines from LLM providers or storage

## Production Checklist

- [ ] Set `allowed_user_id` to your Telegram user ID (don't rely on auto-register in production)
- [ ] Use Turso or a persistent volume for the database
- [ ] Set all API keys via environment variables, not in `oasis.toml`
- [ ] Verify `timezone_offset` matches your timezone
- [ ] Set `OASIS_BRAVE_API_KEY` if you want web search capability
- [ ] Test with a message to your bot after deployment

## Workspace Directory

The `shell_exec` and `file_*` tools operate in a sandboxed workspace directory (default: `~/oasis-workspace`). In Docker, this path is inside the container.

If you need the workspace to persist, mount a volume:

```bash
docker run \
  -v oasis-workspace:/root/oasis-workspace \
  --env-file .env.production \
  oasis
```

Or configure a custom path:

```toml
[brain]
workspace_path = "/data/workspace"
```
