# Configuration Reference

Complete reference for all Oasis configuration options.

## Environment Variables

### Required

| Variable | Description |
|----------|-------------|
| `OASIS_TELEGRAM_TOKEN` | Telegram Bot API token |
| `OASIS_LLM_API_KEY` | API key for the chat LLM provider |
| `OASIS_EMBEDDING_API_KEY` | API key for the embedding provider |

### Optional

| Variable | Description | Fallback |
|----------|-------------|----------|
| `OASIS_INTENT_API_KEY` | Intent classification LLM | `OASIS_LLM_API_KEY` |
| `OASIS_ACTION_API_KEY` | Action/tool-use LLM | `OASIS_LLM_API_KEY` |
| `OASIS_TURSO_URL` | Remote libSQL database URL | Local SQLite |
| `OASIS_TURSO_TOKEN` | Turso auth token | — |
| `OASIS_BRAVE_API_KEY` | Brave Search API key | `web_search` not registered |
| `OASIS_OBSERVER_ENABLED` | Enable OTEL (`true` or `1`) | Disabled |
| `OASIS_CONFIG` | Path to config file | `oasis.toml` |

## TOML Sections

### `[telegram]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `token` | string | `""` | Bot token (prefer env var) |
| `allowed_user_id` | string | `"0"` | Allowed Telegram user ID. `"0"` = auto-register first user as owner |

### `[llm]`

Primary chat LLM.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `"gemini"` | `"gemini"` or `"openai"` |
| `model` | string | `"gemini-2.5-flash"` | Model identifier |
| `api_key` | string | `""` | API key (prefer env var) |

### `[intent]`

Lightweight LLM for intent classification and fact extraction.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `"gemini"` | Provider name |
| `model` | string | `"gemini-2.5-flash-lite"` | Model identifier |
| `api_key` | string | `""` | Falls back to `[llm].api_key` |

### `[action]`

LLM for the agentic tool-use loop.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `""` | Falls back to `[llm].provider` |
| `model` | string | `""` | Falls back to `[llm].model` |
| `api_key` | string | `""` | Falls back to `[llm].api_key` |

### `[embedding]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `"gemini"` | Provider name |
| `model` | string | `"gemini-embedding-001"` | Model identifier |
| `dimensions` | int | `1536` | Vector dimensionality |
| `api_key` | string | `""` | Prefer env var |

### `[database]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `path` | string | `"oasis.db"` | Local SQLite file path |
| `turso_url` | string | `""` | Remote Turso URL (overrides local) |
| `turso_token` | string | `""` | Turso auth token |

### `[brain]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `context_window` | int | `20` | Recent messages in chat context |
| `vector_top_k` | int | `10` | Results from vector search |
| `timezone_offset` | int | `7` | UTC offset (e.g., `7` = WIB, `-5` = EST) |
| `workspace_path` | string | `"~/oasis-workspace"` | Sandboxed directory for shell/file tools |

### `[search]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `brave_api_key` | string | `""` | Brave Search API key |

### `[chunking]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_tokens` | int | `512` | Max tokens per chunk |
| `overlap_tokens` | int | `50` | Overlap between chunks |

### `[observer]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable OTEL observability |

#### `[observer.pricing]`

Override per-model token pricing (per million tokens):

```toml
[observer.pricing."gpt-4o"]
input = 2.50
output = 10.00
```

### `[ollama]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `base_url` | string | `"http://localhost:11434"` | Ollama API base URL |

## OTEL Environment Variables

| Variable | Description |
|----------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector endpoint |
| `OTEL_SERVICE_NAME` | Service name (default `"oasis"`) |
| `OTEL_TRACES_SAMPLER` | Sampling strategy |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` or `http/protobuf` |

## Example: Minimal

```toml
[telegram]
allowed_user_id = 0

[brain]
timezone_offset = -5  # EST
```

```bash
export OASIS_TELEGRAM_TOKEN="..."
export OASIS_LLM_API_KEY="AIza..."
export OASIS_EMBEDDING_API_KEY="AIza..."
```

## Example: Full

```toml
[telegram]
allowed_user_id = "123456789"

[llm]
provider = "gemini"
model = "gemini-2.5-flash"

[intent]
provider = "gemini"
model = "gemini-2.5-flash-lite"

[action]
provider = "gemini"
model = "gemini-2.5-flash"

[embedding]
provider = "gemini"
model = "gemini-embedding-001"
dimensions = 1536

[database]
path = "oasis.db"

[brain]
context_window = 20
vector_top_k = 10
timezone_offset = 7
workspace_path = "/home/user/oasis-workspace"

[chunking]
max_tokens = 512
overlap_tokens = 50

[search]
brave_api_key = ""

[observer]
enabled = false

[ollama]
base_url = "http://localhost:11434"
```
