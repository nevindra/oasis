# oasis-core

Foundation crate providing shared types, configuration, error handling, and utility functions used by all other crates.

## Key Files

- `src/types.rs` - All shared data types
- `src/config.rs` - Configuration loading and structs
- `src/error.rs` - Error type and Result alias
- `src/lib.rs` - Re-exports

## Data Types

```mermaid
classDiagram
    class Document {
        id: String
        source_type: String
        source_ref: Option~String~
        title: Option~String~
        raw_content: String
        created_at: i64
        updated_at: i64
    }

    class Chunk {
        id: String
        document_id: String
        content: String
        chunk_index: i32
        created_at: i64
    }

    class Task {
        id: String
        project_id: Option~String~
        parent_task_id: Option~String~
        title: String
        description: Option~String~
        status: String
        priority: i32
        due_at: Option~i64~
        created_at: i64
        updated_at: i64
    }

    class Conversation {
        id: String
        telegram_chat_id: i64
        created_at: i64
    }

    class Message {
        id: String
        conversation_id: String
        role: String
        content: String
        created_at: i64
    }

    class ChatMessage {
        role: String
        content: String
        tool_calls: Vec~ToolCallRequest~
        tool_call_id: Option~String~
    }

    class ChatRequest {
        messages: Vec~ChatMessage~
        max_tokens: Option~u32~
        temperature: Option~f32~
    }

    class ChatResponse {
        content: String
        tool_calls: Vec~ToolCallRequest~
        usage: Option~Usage~
    }

    class ToolDefinition {
        name: String
        description: String
        parameters: Value
    }

    class ToolCallRequest {
        id: String
        name: String
        arguments: Value
        metadata: Option~Value~
    }

    class ScheduledAction {
        id: String
        description: String
        schedule: String
        tool_calls: String
        synthesis_prompt: Option~String~
        enabled: bool
        last_run: Option~i64~
        next_run: i64
        created_at: i64
    }

    Document "1" --> "*" Chunk
    Conversation "1" --> "*" Message
    Task "*" --> "0..1" Project
```

## ChatMessage Variants

`ChatMessage` serves multiple roles in the LLM conversation:

| Constructor | role | Purpose |
|-------------|------|---------|
| `ChatMessage::text("system", ...)` | system | System prompt |
| `ChatMessage::text("user", ...)` | user | User message |
| `ChatMessage::assistant_tool_calls(...)` | assistant | LLM requesting tool execution |
| `ChatMessage::tool_result(id, ...)` | tool | Tool execution result fed back to LLM |

## Utility Functions

| Function | Purpose |
|----------|---------|
| `new_id()` | Generate ULID-like ID: 12-char hex timestamp + 16-char hex random from `/dev/urandom` |
| `now_unix()` | Current Unix timestamp in seconds |

## Error Handling

`OasisError` is a custom enum (no thiserror/anyhow):

| Variant | Source |
|---------|--------|
| `Config(String)` | Configuration errors |
| `Database(String)` | libSQL/Turso errors |
| `Llm { provider, message }` | LLM API errors |
| `Embedding(String)` | Embedding API errors |
| `Ingest(String)` | Text extraction/ingestion errors |
| `Telegram(String)` | Telegram API errors |
| `Http { status, body }` | HTTP errors with status code |

All crates use `oasis_core::error::Result<T>` (alias for `std::result::Result<T, OasisError>`).

## Config Loading

```mermaid
flowchart LR
    Defaults --> TOML[oasis.toml] --> Env[Environment Variables]
    Env -->|wins| Final[Final Config]
```

Config sections: `telegram`, `llm`, `intent`, `action`, `embedding`, `database`, `chunking`, `brain`, `ollama`.

Fallback rules:
- `intent.api_key` falls back to `llm.api_key`
- `action.provider` + `action.model` fall back to `llm.*`
- `action.api_key` falls back to `llm.api_key`
