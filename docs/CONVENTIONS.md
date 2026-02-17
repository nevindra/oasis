# Oasis Coding Conventions

Rules and patterns that all contributors (human and LLM) must follow. Read this before writing any code in this project.

## Philosophy

Oasis is deliberately minimalist. The project avoids large frameworks, SDK dependencies, and crate bloat. When a standard library solution or hand-rolled implementation exists and is simple enough, it is preferred over adding a dependency.

**Do not add dependencies unless absolutely necessary.** If you think you need a new crate, check whether the project already has a hand-rolled solution first.

## Error Handling

### OasisError Enum

All errors use a custom `OasisError` enum defined in `oasis-core/src/error.rs`. No `anyhow`, no `thiserror`.

```rust
// All crates use this Result alias
use oasis_core::error::{OasisError, Result};

fn do_something() -> Result<String> {
    // ...
}
```

### Rules

- **No `From` impls.** External errors are converted via module-level helper functions or inline `.map_err()` closures.
- **Error messages are lowercase, no trailing period:** `"telegram error: {msg}"`, not `"Telegram error: {msg}."`.
- **Error variants use `String` messages.** External errors are converted via `.to_string()`.
- **The `Llm` variant is the only named-field variant** (`{ provider, message }`). All others are single-field tuple variants wrapping `String`.

```rust
// Module-level helper (preferred for repeated use within a file)
fn map_err(e: libsql::Error) -> OasisError {
    OasisError::Database(e.to_string())
}

// Inline closure (for one-off conversions)
.map_err(|e| OasisError::Llm {
    provider: "anthropic".to_string(),
    message: format!("request failed: {e}"),
})?;
```

### ToolResult is Not a Result

`ToolResult` is a plain struct, not a `Result` type. Tool execution always "succeeds" at the Rust level — errors are encoded as `ToolResult::err(...)` with an `"Error: "` prefix in the output string. This is by design: tool errors should be communicated back to the LLM, not propagated up as Rust errors.

```rust
// Correct
ToolResult::ok("Task created: buy groceries")
ToolResult::err("No tasks found matching 'xyz'")

// Wrong — don't return Result from Tool::execute
```

## Module Structure

### Three-Layer Architecture

oasis-brain follows strict layering: `brain/` → `tool/` → `service/`. Dependency flows downward only. Never import from an upper layer.

```
brain/   (L1) can call → tool/ (L2) and service/ (L3)
tool/    (L2) can call → service/ (L3)
service/ (L3) never calls upward
```

### Brain Decomposition Pattern

The `Brain` struct is defined in `brain/mod.rs`. Behavior is split across **separate files, each containing an `impl Brain` block**. Do not create separate types for Brain subsystems — use method grouping by file.

```rust
// brain/chat.rs
use super::Brain;

impl Brain {
    pub(crate) async fn handle_chat_stream(&self, ...) -> Result<String> {
        // ...
    }
}
```

### mod.rs Pattern

Directory modules use `mod.rs`. Service `mod.rs` files are just lists of `pub mod`:

```rust
// service/mod.rs
pub mod ingest;
pub mod intent;
pub mod llm;
pub mod memory;
pub mod search;
pub mod store;
```

## Visibility

Use `pub(crate)` for methods and fields shared across brain submodules but not part of the public crate API:

```rust
pub struct Brain {
    pub(crate) store: Arc<VectorStore>,
    pub(crate) bot: TelegramBot,
    pub(crate) config: Config,
    // ...
}

impl Brain {
    // Called from other brain/ submodules
    pub(crate) async fn handle_chat_stream(&self, ...) -> Result<String> { ... }

    // Only used within this file
    async fn build_system_prompt(&self, ...) -> String { ... }
}
```

## Import Ordering

Imports follow a 3-group pattern separated by blank lines:

1. **Standard library** (`std::`)
2. **External crates** and **sibling workspace crates** (`oasis_core::`, `reqwest`, `serde_json`, etc.)
3. **Crate-internal** (`crate::`, `super::`)

```rust
use std::sync::Arc;

use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;
use oasis_telegram::bot::TelegramBot;
use serde_json::json;

use super::Brain;
use crate::service::store::VectorStore;
```

### Glob Import for Core Types

`oasis_core::types::*` is used throughout brain and service modules since those types (`ChatMessage`, `ChatRequest`, `Message`, `new_id`, `now_unix`, etc.) are used pervasively.

### Braces for Multi-Item Imports

```rust
use oasis_core::error::{OasisError, Result};
```

## Trait Patterns

### LlmProvider — RPITIT (not object-safe)

`LlmProvider` uses return-position impl trait in trait (RPITIT). It is intentionally **not** object-safe. Providers are dispatched via match, never stored as `dyn LlmProvider`.

```rust
pub trait LlmProvider: Send + Sync {
    fn chat(&self, request: ChatRequest)
        -> impl std::future::Future<Output = Result<ChatResponse>> + Send;
    fn name(&self) -> &str;
}
```

**Do not add `#[async_trait]` to LlmProvider.** Do not try to store providers as trait objects.

### Tool — `#[async_trait]` (object-safe)

`Tool` uses `#[async_trait]` because it must be object-safe — tools are stored as `Box<dyn Tool>` in `ToolRegistry`.

```rust
#[async_trait]
pub trait Tool: Send + Sync {
    fn definitions(&self) -> Vec<ToolDefinition>;
    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult;
}
```

The blanket `impl<T: Tool> Tool for Arc<T>` allows the same tool instance to be shared between Brain fields and the ToolRegistry.

### When to Use Which

| Scenario | Pattern |
|----------|---------|
| Trait object needed (`Box<dyn T>`, `Vec<Box<dyn T>>`) | `#[async_trait]` |
| Static dispatch only (match-based) | RPITIT |

## Adding a New Tool

1. Create a file in `tool/` (e.g., `tool/my_tool.rs`).
2. Define a struct that holds its dependencies (Arc references to services).
3. Implement the `Tool` trait with `definitions()` and `execute()`.
4. Register it in `brain/mod.rs` inside `Brain::new()` by adding it to the `ToolRegistry`.

```rust
// tool/my_tool.rs
use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use serde_json::json;

use crate::tool::{Tool, ToolResult};

pub struct MyTool {
    // dependencies
}

#[async_trait]
impl Tool for MyTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![ToolDefinition {
            name: "my_tool_action".to_string(),
            description: "Does something useful".to_string(),
            parameters: json!({
                "type": "object",
                "properties": {
                    "input": { "type": "string", "description": "The input" }
                },
                "required": ["input"]
            }),
        }]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        match name {
            "my_tool_action" => {
                let input = args["input"].as_str().unwrap_or("");
                // ... do work ...
                ToolResult::ok("Done")
            }
            _ => ToolResult::err(format!("Unknown tool: {name}")),
        }
    }
}
```

### Tool Definition Rules

- Parameters use `serde_json::json!` macro for JSON Schema.
- Tool names use `snake_case`.
- Description should be clear enough for an LLM to decide when to call it.
- A single `Tool` struct can provide **multiple** tool definitions (e.g., `SearchTool` provides `web_search`, `browse_url`, `page_click`, etc.).

### Tool Execute Pattern

If a tool has multiple sub-tools, use `match name` in `execute()`:

```rust
async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
    match name {
        "tool_a" => { /* ... */ }
        "tool_b" => { /* ... */ }
        _ => ToolResult::err(format!("Unknown tool: {name}")),
    }
}
```

Argument extraction uses `args["field"].as_str().unwrap_or("")` — no custom deserialization structs.

## Async Patterns

### Arc<Self> for Background Tasks

`Brain` is wrapped in `Arc<Self>`. Methods that spawn background tasks take `self: &Arc<Self>`:

```rust
impl Brain {
    pub(crate) fn spawn_store(self: &Arc<Self>, conv_id: String, ...) {
        let brain = Arc::clone(self);
        tokio::spawn(async move {
            if let Err(e) = brain.store_message_pair(&conv_id, ...).await {
                log!(" [store] background failed: {e}");
            }
        });
    }
}
```

### Message Processing Concurrency

Each incoming Telegram message is spawned as its own tokio task:

```rust
for update in &updates {
    let brain = Arc::clone(self);
    tokio::spawn(async move {
        if let Err(e) = brain.handle_message(&msg).await {
            log!(" error handling message: {e}");
        }
    });
}
```

### Retry with Exponential Backoff

Transient errors (429, 500-504, connection errors, `STREAM_EXPIRED`) are retried with exponential backoff:

```rust
const MAX_RETRIES: u32 = 3;

for attempt in 0..=MAX_RETRIES {
    if attempt > 0 {
        let delay = std::time::Duration::from_secs(1 << (attempt - 1));
        tokio::time::sleep(delay).await;
    }
    match f().await {
        Ok(val) => return Ok(val),
        Err(e) if is_transient(&e) && attempt < MAX_RETRIES => { /* retry */ }
        Err(e) => return Err(e),
    }
}
```

This pattern is used for both DB operations (VectorStore) and LLM calls (intent LLM).

### Silent Error Handling

Non-critical operations silently discard errors with `let _ =`:

```rust
// Telegram edit during streaming — may fail if content hasn't changed
let _ = self.bot.edit_message(chat_id, msg_id, &text).await;

// Bot command registration — nice-to-have, not critical
let _ = self.bot.set_my_commands(&[...]).await;
```

Only use `let _ =` when failure is expected and non-critical. Always log errors that indicate real problems.

## Logging

### Custom log! Macro

The project uses a custom `log!` macro defined in `oasis-brain/src/lib.rs`. No `log` crate, no `tracing`.

```rust
log!(" [tag] message with {variable}");
```

Output format: `HH:MM:SS oasis:  [tag] message`

### Log Tag Convention

Messages start with a **space, then a bracketed tag**:

```rust
log!(" [recv] from={username} (id={user_id}) chat={}", msg.chat.id);
log!(" [embed] calling {provider}/{model} for {} text(s)", texts.len());
log!(" [tool] {}({})", tool_call.name, tool_call.arguments);
log!(" [store] saving message pair to conversation {conversation_id}");
log!(" [db] retry {attempt}/{MAX_DB_RETRIES} in {}s", delay.as_secs());
```

Common tags: `[recv]`, `[auth]`, `[conv]`, `[text]`, `[route]`, `[intent]`, `[cmd]`, `[embed]`, `[chat-llm]`, `[intent-llm]`, `[tool]`, `[agent]`, `[store]`, `[memory]`, `[db]`, `[search]`, `[send]`, `[integrations]`.

### Outside oasis-brain

In `src/main.rs` (outside the macro's scope), use raw `eprintln!`:

```rust
eprintln!("oasis: starting...");
eprintln!("fatal: failed to load config: {e}");
```

## String Conventions

### Inline Format Variables

Use Rust 2021 capture syntax for format strings:

```rust
// Correct
format!("unknown provider: '{other}'")
log!(" [embed] calling {provider}/{model} for {} text(s)", texts.len());

// Avoid positional arguments when captures are available
```

### String Building

Use `String::new()` + `push_str()` with `&format!()` for building long strings:

```rust
let mut system = format!("Current date: {today}\n");
if !context.is_empty() {
    system.push_str(&format!("\n{context}\n"));
}
```

### Constructors

Use `impl Into<String>` for constructor parameters that take strings:

```rust
pub fn text(role: impl Into<String>, content: impl Into<String>) -> Self {
    Self {
        role: role.into(),
        content: content.into(),
    }
}
```

### String Conversion

Use `.to_string()` for `&str` → `String`. Not `.to_owned()`, not `String::from()`.

## Type Conventions

### Shared Types

All shared types live in `oasis-core/src/types.rs`. Derive order is always:

```rust
#[derive(Debug, Clone, Serialize, Deserialize)]
```

### Field Rules

- All fields are `pub`.
- IDs are `String` (ULID-like from `new_id()`).
- Timestamps are `i64` (unix seconds from `now_unix()`).
- Nullable fields use `Option<String>`.
- Schemaless JSON uses `serde_json::Value`.
- No `#[serde(rename_all)]` — field names match JSON keys exactly.

### ID Generation

Use `new_id()` from `oasis_core::types` for all entity IDs. Do not use UUID crates or other ID schemes.

### Date/Time

Use `now_unix()` from `oasis_core::types` for timestamps. Date math uses hand-rolled functions in `oasis-brain/src/util.rs`. Do not add `chrono` or `time` crates.

## Config Conventions

### Adding a New Config Field

1. Add the field to the appropriate sub-config struct in `oasis-core/src/config.rs`.
2. Add a `#[serde(default = "fn_name")]` attribute.
3. Create a module-level default function (not a method).
4. Update the `Default` impl to call the same default function.
5. If it should be overridable via env var, add the override in `Config::load()`.

```rust
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MyConfig {
    #[serde(default = "default_my_value")]
    pub my_value: String,
}

fn default_my_value() -> String {
    "default".to_string()
}

impl Default for MyConfig {
    fn default() -> Self {
        Self {
            my_value: default_my_value(),
        }
    }
}
```

### Env Var Naming

All env vars use the `OASIS_` prefix, uppercase, underscore-separated: `OASIS_TELEGRAM_TOKEN`, `OASIS_LLM_API_KEY`.

## Database Conventions

### Fresh Connections

Each VectorStore method creates a fresh connection via `self.db.connect()`. Do not cache or reuse connections — this avoids `STREAM_EXPIRED` errors on Turso.

### Module-Level Error Mapper

Each service file that talks to libSQL defines its own error mapper:

```rust
fn map_err(e: libsql::Error) -> OasisError {
    OasisError::Database(e.to_string())
}
```

### DB Retry

Wrap database operations in the `with_retry` pattern for transient errors. Check `is_transient_db_error()` before retrying.

## Testing

### Test Structure

Tests go at the bottom of the source file in a `#[cfg(test)] mod tests` block:

```rust
#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_descriptive_name() {
        // ...
    }
}
```

### Rules

- All tests are **synchronous** `#[test]`. No `#[tokio::test]` — tests focus on pure functions and validation logic.
- Test naming: `test_<descriptive_snake_case_name>`.
- Helper setup functions are plain functions in the test module, not fixtures or macros.
- Assertions: `assert!`, `assert_eq!`, `assert!(matches!(...))`. No custom assertion macros.
- Test both valid and invalid inputs (e.g., `test_blocked_commands` + `test_allowed_commands`).

## LLM Provider Conventions

### Adding a New Provider

1. Create a file in `oasis-llm/src/` (e.g., `my_provider.rs`).
2. Implement `LlmProvider` and/or `EmbeddingProvider` using RPITIT (not `#[async_trait]`).
3. Use raw HTTP via `reqwest`. No SDK dependencies.
4. Add the match arm in `oasis-brain/src/service/llm.rs` dispatch methods.
5. Add the provider name to the error message listing supported providers.

```rust
// oasis-llm/src/my_provider.rs
pub struct MyProviderLlm {
    client: Client,
    api_key: String,
    model: String,
}

impl LlmProvider for MyProviderLlm {
    fn chat(&self, request: ChatRequest)
        -> impl std::future::Future<Output = Result<ChatResponse>> + Send {
        async move {
            // raw HTTP call
        }
    }
    fn name(&self) -> &str { "my_provider" }
}
```

### Streaming

All streaming implementations use SSE (Server-Sent Events):

1. Send request with `stream: true` parameter.
2. Read response as streaming bytes.
3. Parse SSE `data:` lines.
4. Send text deltas via `tx.send(chunk)`.
5. Return final `ChatResponse` with usage stats.

## Telegram Conventions

### HTML, Not Markdown

Always use `parse_mode: "HTML"` for formatted output. Convert markdown to HTML via `pulldown-cmark`. Telegram's legacy "Markdown" parse_mode does not support `**bold**`, `###` headers, or other standard markdown.

### Streaming Edits

- **Intermediate edits**: plain text (markdown may be incomplete mid-stream).
- **Final edit**: HTML via `edit_message_formatted()`.
- **Edit errors**: silently ignored with `let _ =`.
- **Edit rate**: max once per second to avoid Telegram rate limits.

### Message Length

Telegram has a 4096-character limit. `split_message()` handles splitting at newline boundaries.

## Constants

Place constants at the top of the file, after imports:

```rust
const MAX_ITERATIONS: usize = 10;
const PAGE_FETCH_TIMEOUT: u64 = 10;
const MAX_PAGE_CHARS: usize = 12_000;
```

Use `_` separators for large numbers: `5_242_880` not `5242880`.

## Things to Never Do

- **Do not add `anyhow` or `thiserror`.** Use `OasisError` with `map_err`.
- **Do not add `chrono` or `time`.** Use `now_unix()` and `util.rs` date math.
- **Do not add UUID crates.** Use `new_id()`.
- **Do not add bot frameworks.** The Telegram client is hand-rolled.
- **Do not add LLM SDK crates.** All providers use raw HTTP.
- **Do not use `dyn LlmProvider`.** Use match-based dispatch.
- **Do not add `#[async_trait]` to `LlmProvider`.** It uses RPITIT intentionally.
- **Do not use `log` or `tracing` crates.** Use the custom `log!` macro.
- **Do not cache database connections.** Create fresh connections per call.
- **Do not return `Result` from `Tool::execute`.** Return `ToolResult::ok()` or `ToolResult::err()`.
- **Do not use Telegram's `parse_mode: "Markdown"`.** Always use HTML.
- **Do not add `[workspace.dependencies]`.** Each crate declares its own versions.
- **Do not use `reqwest` with default features.** Always use `default-features = false, features = ["rustls-tls", ...]`.
