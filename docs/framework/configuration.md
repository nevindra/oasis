# Configuration Reference

Oasis configuration follows a three-layer cascade: **defaults** -> **`oasis.toml`** -> **environment variables**. Environment variables always win.

## Loading Order

1. `config.Default()` applies built-in defaults for every field
2. If `oasis.toml` exists (or the file specified by `OASIS_CONFIG`), it is parsed and merged on top of defaults
3. Environment variables with the `OASIS_` prefix override individual fields

```go
// Internally:
cfg := config.Default()       // 1. Defaults
toml.Unmarshal(data, &cfg)    // 2. TOML overlay
// 3. Env var overrides applied one by one
```

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
| `OASIS_INTENT_API_KEY` | API key for the intent classification LLM | Falls back to `OASIS_LLM_API_KEY` |
| `OASIS_ACTION_API_KEY` | API key for the action/tool-use LLM | Falls back to `OASIS_LLM_API_KEY` |
| `OASIS_TURSO_URL` | Remote libSQL (Turso) database URL | Uses local SQLite file |
| `OASIS_TURSO_TOKEN` | Authentication token for Turso | - |
| `OASIS_BRAVE_API_KEY` | Brave Search API key (enables `web_search` tool) | Tool not registered |
| `OASIS_OBSERVER_ENABLED` | Enable OTEL observability (`true` or `1`) | Disabled |
| `OASIS_CONFIG` | Path to config file | `oasis.toml` |

## TOML Configuration Sections

### `[telegram]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `token` | string | `""` | Bot token (prefer `OASIS_TELEGRAM_TOKEN` env var) |
| `allowed_user_id` | string | `"0"` | Telegram user ID allowed to interact. `"0"` = auto-register the first user as owner |

**Auto-registration**: When `allowed_user_id` is `"0"`, the first user to message the bot is permanently registered as the owner. Their user ID is stored in the database `config` table under the key `owner_user_id`.

### `[llm]`

The primary chat LLM used for streaming conversational responses.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `"gemini"` | LLM provider name |
| `model` | string | `"gemini-2.5-flash"` | Model identifier |
| `api_key` | string | `""` | API key (prefer `OASIS_LLM_API_KEY` env var) |

Supported providers: `"gemini"`, `"openai"` (OpenAI-compatible endpoints).

### `[intent]`

The lightweight LLM used for intent classification and fact extraction. Typically a smaller/faster model.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `"gemini"` | Provider name |
| `model` | string | `"gemini-2.5-flash-lite"` | Model identifier |
| `api_key` | string | `""` | API key (falls back to `[llm].api_key`) |

### `[action]`

The LLM used for the agentic tool-use loop. Needs to support function calling.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `""` | Provider name (falls back to `[llm].provider`) |
| `model` | string | `""` | Model identifier (falls back to `[llm].model`) |
| `api_key` | string | `""` | API key (falls back to `[llm].api_key`) |

### `[embedding]`

The embedding provider for vector search.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `"gemini"` | Provider name |
| `model` | string | `"gemini-embedding-001"` | Model identifier |
| `dimensions` | int | `1536` | Embedding vector dimensionality |
| `api_key` | string | `""` | API key (prefer `OASIS_EMBEDDING_API_KEY` env var) |

### `[database]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `path` | string | `"oasis.db"` | Path to the local SQLite database file |
| `turso_url` | string | `""` | Remote Turso database URL (overrides local path) |
| `turso_token` | string | `""` | Turso authentication token |

When `turso_url` is set, Oasis connects to the remote Turso database instead of using the local SQLite file.

### `[brain]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `context_window` | int | `20` | Number of recent messages included in chat context |
| `vector_top_k` | int | `10` | Number of results returned from vector searches |
| `timezone_offset` | int | `7` | UTC offset in hours (e.g., `7` for UTC+7 WIB, `-5` for EST) |
| `workspace_path` | string | `"~/oasis-workspace"` | Sandboxed directory for shell and file tools |

### `[search]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `brave_api_key` | string | `""` | Brave Search API key. When empty, the `web_search` tool is not registered |

### `[chunking]`

Controls the document ingestion chunking strategy. These settings configure the default `RecursiveChunker` used by the reference app. When using the `Ingestor` API directly, configure chunking via `WithMaxTokens()`, `WithOverlapTokens()`, and other `ChunkerOption`s.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_tokens` | int | `512` | Maximum tokens per chunk (~4 chars per token) |
| `overlap_tokens` | int | `50` | Token overlap between consecutive chunks |

### `[observer]`

OTEL-based observability for LLM calls, tool executions, and embeddings. When enabled, wraps Provider, EmbeddingProvider, and Tool with instrumented versions that emit traces, metrics, and logs via OpenTelemetry.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Master switch. Also settable via `OASIS_OBSERVER_ENABLED=true` env var. |

#### `[observer.pricing]`

Override or extend the built-in per-model token pricing table. Prices are per million tokens.

```toml
[observer.pricing."gpt-4o"]
input = 2.50
output = 10.00

[observer.pricing."my-custom-model"]
input = 1.00
output = 3.00
```

Built-in defaults include pricing for common Gemini, OpenAI, and Anthropic models. Unknown models report cost as `0.0`.

**OTEL configuration** uses standard environment variables:

| Variable | Description |
|----------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector endpoint (e.g. `http://localhost:4318`) |
| `OTEL_SERVICE_NAME` | Service name (defaults to `"oasis"`) |
| `OTEL_TRACES_SAMPLER` | Trace sampling strategy |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` or `http/protobuf` |

### `[ollama]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `base_url` | string | `"http://localhost:11434"` | Ollama API base URL |

## API Key Fallback Logic

The configuration system applies fallback logic after loading, so you can use a single API key for all LLM operations:

```
Intent API key:
  OASIS_INTENT_API_KEY -> [intent].api_key -> [llm].api_key

Action provider:
  [action].provider -> [llm].provider

Action model:
  [action].model -> [llm].model

Action API key:
  OASIS_ACTION_API_KEY -> [action].api_key -> [llm].api_key
```

**Minimal setup**: If you use Gemini for everything, you only need one API key:

```bash
export OASIS_TELEGRAM_TOKEN="..."
export OASIS_LLM_API_KEY="your-gemini-key"
export OASIS_EMBEDDING_API_KEY="your-gemini-key"
```

## Three LLM Model Strategy

Oasis uses three separately configurable LLM models, each optimized for its role:

| Role | Config Section | Default Model | Purpose |
|------|---------------|---------------|---------|
| **Chat** | `[llm]` | gemini-2.5-flash | Streaming conversational responses. Optimized for quality and speed. |
| **Intent** | `[intent]` | gemini-2.5-flash-lite | Intent classification + fact extraction. Smallest/fastest model. |
| **Action** | `[action]` | (same as `[llm]`) | Agentic tool-use loop with function calling. Needs tool support. |

This separation lets you optimize cost and latency: use a fast/cheap model for intent classification (which runs on every message), and a more capable model for chat and tool-use.

## Example Configuration

### Minimal (Gemini for everything)

```toml
# oasis.toml
[telegram]
allowed_user_id = 0

[brain]
timezone_offset = -5  # EST
```

```bash
# .env
export OASIS_TELEGRAM_TOKEN="123456:ABC..."
export OASIS_LLM_API_KEY="AIza..."
export OASIS_EMBEDDING_API_KEY="AIza..."
```

### Full Configuration

```toml
# oasis.toml
[telegram]
allowed_user_id = "123456789"

[llm]
provider = "gemini"
model = "gemini-2.5-flash-preview-09-2025"

[intent]
provider = "gemini"
model = "gemini-2.5-flash-lite"

[action]
provider = "gemini"
model = "gemini-2.5-flash-preview-09-2025"

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

[ollama]
base_url = "http://localhost:11434"
```

## Conditional Features

Some features are only enabled when their credentials are configured:

| Feature | Required Config | Effect |
|---------|----------------|--------|
| Web search | `OASIS_BRAVE_API_KEY` | `web_search` tool registered |
| Long-term memory | MemoryStore passed to App | Fact extraction + memory injection |
| Observability | `OASIS_OBSERVER_ENABLED=true` | OTEL traces, metrics, and logs for LLM/tool/embedding calls |

If a required credential is missing, the feature is silently disabled and its tools are not registered in the ToolRegistry.
