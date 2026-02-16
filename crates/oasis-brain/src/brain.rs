use std::sync::Arc;

use crate::intent::{parse_intent, Intent};
use oasis_core::config::Config;
use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;
use oasis_ingest::pipeline::IngestPipeline;
use oasis_llm::anthropic::AnthropicLlm;
use oasis_llm::gemini::{GeminiEmbedding, GeminiLlm};
use oasis_llm::ollama::{OllamaEmbedding, OllamaLlm};
use oasis_llm::openai::{OpenAiEmbedding, OpenAiLlm};
use oasis_llm::provider::{EmbeddingProvider, LlmProvider};
use oasis_tasks::manager::TaskManager;
use oasis_telegram::bot::TelegramBot;
use oasis_telegram::types::TelegramDocument;
use oasis_memory::memory::MemoryStore;
use oasis_search::search::WebSearch;
use oasis_tools::tool::{builtin_tool_definitions, ToolResult};
use oasis_vector::store::VectorStore;

/// Intent classification prompt sent to Flash-Lite.
/// Simplified to just Chat vs Action — the main LLM handles tool selection.
const INTENT_SYSTEM_PROMPT: &str = r#"You are an intent classifier for a personal assistant. Classify the user message into exactly one of two intents.

Return a JSON object with a single "intent" field:

1. **chat** — Conversation, questions, opinions, recommendations, explanations, or anything the assistant can answer from its own knowledge. This includes: "what is X?", "recommend me Y", "what do you think about Z?", "tell me about...", follow-up questions, casual talk, greetings.
   Return: `{"intent":"chat"}`

2. **action** — The user explicitly wants to CREATE, UPDATE, DELETE, or SEARCH for something using a tool. Examples: "buatkan task ...", "tandai ... selesai", "hapus task ...", "cari di internet ...", "ingatkan aku ...", "cari di knowledge base".
   Return: `{"intent":"action"}`

## Rules
- If the user is asking a question or having a conversation, it's CHAT — even if the topic involves tasks, books, schedules, etc.
- Action is ONLY when the user wants to PERFORM an operation (create, update, delete, search, save).
- If in doubt, prefer CHAT.
- Respond with ONLY the JSON object, no extra text.
- The user may write in English or Indonesian."#;

/// Log with HH:MM:SS timestamp.
macro_rules! log {
    ($($arg:tt)*) => {{
        let secs = now_unix();
        let h = (secs % 86400) / 3600;
        let m = (secs % 3600) / 60;
        let s = secs % 60;
        eprintln!("{h:02}:{m:02}:{s:02} oasis: {}", format_args!($($arg)*));
    }};
}

/// The main orchestration struct that ties all components together.
///
/// Brain owns the vector store, task manager, ingestion pipeline,
/// Telegram bot, and configuration. LLM and embedding providers are
/// created on-the-fly based on config to avoid issues with async trait
/// objects.
pub struct Brain {
    store: VectorStore,
    tasks: TaskManager,
    memory: MemoryStore,
    search: WebSearch,
    pipeline: IngestPipeline,
    bot: TelegramBot,
    config: Config,
}

/// A chunk of text from a web search result, tagged with its source and relevance score.
struct RankedChunk {
    text: String,
    source_index: usize,
    source_title: String,
    score: f32,
}

/// Cosine similarity between two vectors.
fn cosine_similarity(a: &[f32], b: &[f32]) -> f32 {
    let dot: f32 = a.iter().zip(b.iter()).map(|(x, y)| x * y).sum();
    let norm_a: f32 = a.iter().map(|x| x * x).sum::<f32>().sqrt();
    let norm_b: f32 = b.iter().map(|x| x * x).sum::<f32>().sqrt();
    if norm_a == 0.0 || norm_b == 0.0 {
        return 0.0;
    }
    dot / (norm_a * norm_b)
}

impl Brain {
    /// Initialize a new Brain with the given configuration.
    ///
    /// Creates the VectorStore (which initializes all database tables),
    /// opens a separate database connection for TaskManager, and sets up
    /// the ingestion pipeline and Telegram bot.
    pub async fn new(config: Config) -> Result<Self> {
        // VectorStore creates the DB and all tables
        let store = if !config.database.turso_url.is_empty() {
            VectorStore::new_remote(&config.database.turso_url, &config.database.turso_token)
                .await?
        } else {
            VectorStore::new(&config.database.path).await?
        };

        // Open a separate Database handle for TaskManager
        let task_db = if !config.database.turso_url.is_empty() {
            libsql::Builder::new_remote(
                config.database.turso_url.clone(),
                config.database.turso_token.clone(),
            )
            .build()
            .await
            .map_err(|e| OasisError::Database(e.to_string()))?
        } else {
            libsql::Builder::new_local(&config.database.path)
                .build()
                .await
                .map_err(|e| OasisError::Database(e.to_string()))?
        };
        let tasks = TaskManager::new(task_db);

        // Open a separate Database handle for MemoryStore
        let memory_db = if !config.database.turso_url.is_empty() {
            libsql::Builder::new_remote(
                config.database.turso_url.clone(),
                config.database.turso_token.clone(),
            )
            .build()
            .await
            .map_err(|e| OasisError::Database(e.to_string()))?
        } else {
            libsql::Builder::new_local(&config.database.path)
                .build()
                .await
                .map_err(|e| OasisError::Database(e.to_string()))?
        };
        let memory = MemoryStore::new(memory_db);
        memory.init().await?;

        let pipeline =
            IngestPipeline::new(config.chunking.max_tokens, config.chunking.overlap_tokens);
        let bot = TelegramBot::new(
            config.telegram.token.clone(),
            config.telegram.allowed_user_id,
        );

        let search = WebSearch::new().await?;

        Ok(Self {
            store,
            tasks,
            memory,
            search,
            pipeline,
            bot,
            config,
        })
    }

    /// The main run loop: long-poll Telegram for updates, handle each message.
    ///
    /// This method runs indefinitely. On recoverable errors (network, API, etc.)
    /// it logs the error and continues. Only startup failures cause a return.
    pub async fn run(self: &Arc<Self>) -> Result<()> {
        // Verify the bot token on startup
        let me = self.bot.get_me().await?;
        log!(
            "bot started as @{}",
            me.username.as_deref().unwrap_or("unknown")
        );

        // Register bot commands with Telegram
        let _ = self.bot.set_my_commands(&[
            ("new", "Start a new conversation"),
        ]).await;

        // Spawn the scheduled actions background loop
        {
            let brain = Arc::clone(self);
            tokio::spawn(async move {
                brain.run_scheduled_actions_loop().await;
            });
        }

        // Retrieve the last known update offset, or start from 0
        let mut offset: i64 = self
            .store
            .get_config("telegram_offset")
            .await?
            .and_then(|s| s.parse().ok())
            .unwrap_or(0);

        loop {
            let updates = match self.bot.get_updates(offset, 30).await {
                Ok(u) => u,
                Err(e) => {
                    log!(" error polling updates: {e}");
                    tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                    continue;
                }
            };

            for update in &updates {
                // Advance offset past this update
                if update.update_id >= offset {
                    offset = update.update_id + 1;
                }

                if let Some(ref msg) = update.message {
                    if let Err(e) = self.handle_message(msg).await {
                        log!(" error handling message: {e}");
                    }
                }
            }

            // Persist the offset so we don't reprocess on restart
            if !updates.is_empty() {
                let _ = self
                    .store
                    .set_config("telegram_offset", &offset.to_string())
                    .await;
            }
        }
    }

    /// Check if a user is authorized. Uses DB-stored owner_user_id first,
    /// falls back to config. If neither is set (both 0), the first user
    /// to message auto-registers as owner.
    async fn is_owner(&self, user_id: i64) -> Result<bool> {
        // Check DB first
        if let Some(owner_str) = self.store.get_config("owner_user_id").await? {
            if let Ok(owner_id) = owner_str.parse::<i64>() {
                return Ok(owner_id == user_id);
            }
        }

        // Fall back to config
        if self.config.telegram.allowed_user_id != 0 {
            return Ok(self.config.telegram.allowed_user_id == user_id);
        }

        // No owner set — register this user as owner
        self.store
            .set_config("owner_user_id", &user_id.to_string())
            .await?;
        log!(" registered owner user_id={user_id}");
        Ok(true)
    }

    // ─── Intent classification ────────────────────────────────────────

    /// Classify a user message into a structured Intent using the intent LLM (Flash-Lite).
    async fn classify_intent(&self, message: &str) -> Result<Intent> {
        let tz_offset = self.config.brain.timezone_offset;
        let (now_str, tz_str) = format_now_with_tz(tz_offset);

        let system = INTENT_SYSTEM_PROMPT
            .replace("{now}", &now_str)
            .replace("{tz}", &tz_str);

        let request = ChatRequest {
            messages: vec![
                ChatMessage::text("system", system),
                ChatMessage::text("user", message),
            ],
            max_tokens: Some(256),
            temperature: Some(0.0),
        };

        let response = self.chat_intent_llm(request).await?;
        let intent = parse_intent(&response.content);
        Ok(intent)
    }

    /// Dispatch an LLM request to the intent model (configured separately from main LLM).
    /// Retries up to 3 times on transient errors (429, 500, 502, 503, 504) with exponential backoff.
    async fn chat_intent_llm(&self, request: ChatRequest) -> Result<ChatResponse> {
        let provider = &self.config.intent.provider;
        let model = &self.config.intent.model;

        let max_retries = 3;
        let mut last_err = None;

        for attempt in 0..=max_retries {
            if attempt > 0 {
                let delay = std::time::Duration::from_secs(1 << (attempt - 1)); // 1s, 2s, 4s
                log!(" [intent-llm] retry {attempt}/{max_retries} in {}s", delay.as_secs());
                tokio::time::sleep(delay).await;
            }

            log!(" [intent-llm] calling {provider}/{model}");

            let result = match provider.as_str() {
                "gemini" => {
                    let p = GeminiLlm::new(
                        self.config.intent.api_key.clone(),
                        model.clone(),
                    );
                    p.chat(request.clone()).await
                }
                "anthropic" => {
                    let p = AnthropicLlm::new(
                        self.config.intent.api_key.clone(),
                        model.clone(),
                    );
                    p.chat(request.clone()).await
                }
                "openai" => {
                    let p = OpenAiLlm::new(
                        self.config.intent.api_key.clone(),
                        model.clone(),
                    );
                    p.chat(request.clone()).await
                }
                "ollama" => {
                    let p = OllamaLlm::new(
                        self.config.ollama.base_url.clone(),
                        model.clone(),
                    );
                    p.chat(request.clone()).await
                }
                other => {
                    return Err(OasisError::Config(format!(
                        "unknown intent provider: '{other}'"
                    )));
                }
            };

            match result {
                Ok(resp) => {
                    log!(" [intent-llm] OK: {}", &resp.content[..resp.content.len().min(120)]);
                    if let Some(ref u) = resp.usage {
                        log!(" [intent-llm] tokens: in={} out={}", u.input_tokens, u.output_tokens);
                    }
                    return Ok(resp);
                }
                Err(ref e) if is_transient_error(e) && attempt < max_retries => {
                    log!(" [intent-llm] transient error: {e}");
                    last_err = Some(result.unwrap_err());
                }
                Err(e) => {
                    log!(" [intent-llm] ERROR: {e}");
                    return Err(e);
                }
            }
        }

        Err(last_err.unwrap_or_else(|| OasisError::Llm {
            provider: provider.clone(),
            message: "max retries exceeded".to_string(),
        }))
    }

    // ─── Message handling ─────────────────────────────────────────────

    /// Handle a single incoming Telegram message.
    ///
    /// Performs auth check, gets or creates conversation. Document uploads
    /// and URLs are handled structurally (no LLM needed). All text messages
    /// are classified via the intent LLM and dispatched accordingly.
    async fn handle_message(self: &Arc<Self>, msg: &oasis_telegram::types::TelegramMessage) -> Result<()> {
        let user_id = msg.from.as_ref().map(|u| u.id).unwrap_or(0);
        let username = msg.from.as_ref().and_then(|u| u.username.as_deref()).unwrap_or("?");
        log!(" [recv] from={username} (id={user_id}) chat={}", msg.chat.id);

        if !self.is_owner(user_id).await? {
            log!(" [auth] DENIED user_id={user_id}");
            return Ok(());
        }
        log!(" [auth] OK");

        let chat_id = msg.chat.id;
        let _ = self.bot.send_typing(chat_id).await;

        let conversation = self.store.get_or_create_conversation(chat_id).await?;
        log!(" [conv] id={}", conversation.id);

        // Handle document uploads (structural — no intent classification needed)
        if let Some(ref doc) = msg.document {
            let fname = doc.file_name.as_deref().unwrap_or("?");
            log!(" [file] name={fname} id={}", doc.file_id);
            let response = self.handle_file(doc, &conversation.id).await?;
            log!(" [send] {} chars", response.len());
            self.bot.send_message(chat_id, &response).await?;
            let user_text = msg.caption.as_deref().unwrap_or("[file upload]").to_string();
            self.spawn_store(conversation.id.clone(), user_text, response);
            return Ok(());
        }

        let text = match msg.text.as_deref() {
            Some(t) if !t.is_empty() => t,
            _ => {
                log!(" [skip] empty message");
                return Ok(());
            }
        };
        log!(" [text] \"{}\"", &text[..text.len().min(80)]);

        // /new command — start a new conversation (silent)
        if text.trim() == "/new" {
            self.store.create_new_conversation(chat_id).await?;
            log!(" [cmd] /new — created new conversation");
            return Ok(());
        }

        // URL messages (structural — no intent classification needed)
        if text.starts_with("http://") || text.starts_with("https://") {
            log!(" [route] url");
            let response = self.handle_url(text, &conversation.id).await?;
            log!(" [send] {} chars", response.len());
            self.bot.send_message(chat_id, &response).await?;
            self.spawn_store(conversation.id.clone(), text.to_string(), response);
            return Ok(());
        }

        // Classify intent via LLM
        let intent = self.classify_intent(text).await?;
        log!(" [intent] {intent:?}");

        // Dispatch based on intent
        match intent {
            Intent::Chat => {
                log!(" [route] chat");
                let recent_messages = self
                    .store
                    .get_recent_messages(&conversation.id, self.config.brain.context_window)
                    .await?;
                log!(" [history] {} recent messages", recent_messages.len());
                let response = self
                    .handle_chat_stream(chat_id, text, &recent_messages, "")
                    .await?;
                self.spawn_store(conversation.id.clone(), text.to_string(), response);
            }
            Intent::Action => {
                log!(" [route] action");
                let response = self.handle_action(chat_id, text, &conversation.id).await?;
                self.spawn_store(conversation.id.clone(), text.to_string(), response);
            }
        }

        Ok(())
    }

    /// Spawn a background task to embed + store a message pair, then extract user facts.
    fn spawn_store(self: &Arc<Self>, conv_id: String, user_text: String, assistant_text: String) {
        let brain = Arc::clone(self);
        tokio::spawn(async move {
            if let Err(e) = brain.store_message_pair(&conv_id, &user_text, &assistant_text).await {
                log!(" [store] background failed: {e}");
            }

            // Extract and store user facts from this conversation turn
            brain.extract_and_store_facts(&user_text, &assistant_text).await;
        });
    }

    /// Extract user facts from a conversation turn using the intent LLM and store them.
    async fn extract_and_store_facts(&self, user_text: &str, assistant_text: &str) {
        // Fetch existing facts so the LLM can skip re-extracting them
        let existing = match self.memory.get_top_facts(30).await {
            Ok(facts) => facts,
            Err(_) => Vec::new(),
        };

        let mut prompt = oasis_memory::memory::EXTRACT_FACTS_PROMPT.to_string();
        if !existing.is_empty() {
            prompt.push_str("\n\n## Already known facts (do NOT re-extract these)\n");
            for f in &existing {
                prompt.push_str(&format!("- {}\n", f.fact));
            }
            prompt.push_str("\nOnly extract NEW facts not already covered above.");
        }

        let conversation_turn = format!("User: {user_text}\nAssistant: {assistant_text}");

        let request = ChatRequest {
            messages: vec![
                ChatMessage::text("system", prompt),
                ChatMessage::text("user", conversation_turn),
            ],
            max_tokens: Some(512),
            temperature: Some(0.0),
        };

        let response = match self.chat_intent_llm(request).await {
            Ok(r) => r,
            Err(e) => {
                log!(" [memory] fact extraction failed: {e}");
                return;
            }
        };

        let facts = MemoryStore::parse_extracted_facts(&response.content);
        if !facts.is_empty() {
            log!(" [memory] extracted {} fact(s)", facts.len());
        }

        for fact in &facts {
            if let Err(e) = self.memory.upsert_fact(&fact.fact, &fact.category, None).await {
                log!(" [memory] failed to upsert fact: {e}");
            }
        }
    }

    // ─── Intent handlers ──────────────────────────────────────────────

    /// Chunk fetched web pages, embed them, and rank by cosine similarity to
    /// the query. Returns the top chunks sorted by relevance (highest first).
    async fn rank_search_results(
        &self,
        query: &str,
        results: &[oasis_search::search::SearchResultWithContent],
    ) -> Vec<RankedChunk> {
        // Chunk config: ~500 chars per chunk, no overlap (we want independent chunks)
        let chunk_config = oasis_ingest::chunker::ChunkerConfig {
            max_chars: 500,
            overlap_chars: 0,
        };

        // Build tagged chunks from all results
        let mut tagged_chunks: Vec<RankedChunk> = Vec::new();
        for (i, r) in results.iter().enumerate() {
            // Always include the snippet as a chunk (it's Google's summary)
            if !r.result.snippet.is_empty() {
                tagged_chunks.push(RankedChunk {
                    text: r.result.snippet.clone(),
                    source_index: i,
                    source_title: r.result.title.clone(),
                    score: 0.0,
                });
            }

            // Chunk the page content
            if let Some(ref content) = r.content {
                let chunks = oasis_ingest::chunker::chunk_text(content, &chunk_config);
                for chunk_text in chunks {
                    if chunk_text.len() < 50 {
                        continue; // skip tiny chunks
                    }
                    tagged_chunks.push(RankedChunk {
                        text: chunk_text,
                        source_index: i,
                        source_title: r.result.title.clone(),
                        score: 0.0,
                    });
                }
            }
        }

        if tagged_chunks.is_empty() {
            return tagged_chunks;
        }

        log!(
            " [search] chunked into {} pieces, embedding...",
            tagged_chunks.len()
        );

        // Embed query + all chunks in one batch
        let mut texts: Vec<&str> = vec![query];
        for chunk in &tagged_chunks {
            texts.push(&chunk.text);
        }

        let embeddings = match self.embed_text(&texts).await {
            Ok(e) => e,
            Err(e) => {
                log!(" [search] embedding failed: {e}, falling back to unranked");
                // Return chunks unranked (first N)
                tagged_chunks.truncate(8);
                return tagged_chunks;
            }
        };

        // First embedding is the query, rest are chunks
        let query_vec = &embeddings[0];
        for (i, chunk) in tagged_chunks.iter_mut().enumerate() {
            chunk.score = cosine_similarity(query_vec, &embeddings[i + 1]);
        }

        // Sort by score descending
        tagged_chunks.sort_by(|a, b| b.score.partial_cmp(&a.score).unwrap_or(std::cmp::Ordering::Equal));

        log!(
            " [search] top score: {:.3}, bottom: {:.3}",
            tagged_chunks.first().map(|c| c.score).unwrap_or(0.0),
            tagged_chunks.last().map(|c| c.score).unwrap_or(0.0),
        );

        tagged_chunks
    }

    /// Handle task creation.
    async fn handle_task_create(
        &self,
        title: &str,
        description: Option<&str>,
        due: Option<&str>,
        priority: i32,
    ) -> Result<String> {
        // Parse due date string to unix timestamp if present
        let tz = self.config.brain.timezone_offset;
        let due_at = due.and_then(|d| parse_date_to_unix(d, tz));

        let task = self
            .tasks
            .create_task(title, None, None, description, priority, due_at)
            .await?;

        let short = &task.id[task.id.len().saturating_sub(6)..];
        let mut response = format!("Task created: **{}** (#{short})", task.title);
        if let Some(desc) = &task.description {
            response.push_str(&format!("\nDescription: {desc}"));
        }
        if let Some(due_ts) = task.due_at {
            response.push_str(&format!("\nDue: {}", format_due(due_ts, tz)));
        }
        if task.priority > 0 {
            let prio_label = match task.priority {
                1 => "low",
                2 => "medium",
                3 => "high",
                _ => "normal",
            };
            response.push_str(&format!("\nPriority: {prio_label}"));
        }
        response.push_str(&format!("\nStatus: {}", task.status));

        Ok(response)
    }

    /// Handle task queries -- list active tasks or filter by status.
    async fn handle_task_query(&self, filter: Option<&str>) -> Result<String> {
        // If a specific status filter is given, use it
        let status_filter = filter.and_then(|f| {
            let lower = f.to_lowercase();
            if lower.contains("done") || lower.contains("complete") {
                Some("done")
            } else if lower.contains("progress") || lower.contains("active") || lower.contains("doing") {
                Some("in_progress")
            } else if lower.contains("todo") || lower.contains("pending") {
                Some("todo")
            } else {
                None
            }
        });

        // First try to get the formatted active task summary
        if status_filter.is_none() {
            let summary = self.tasks.get_active_task_summary(self.config.brain.timezone_offset).await?;
            return Ok(summary);
        }

        // If a specific filter was requested, list those tasks
        let tasks = self.tasks.list_tasks(None, status_filter).await?;

        if tasks.is_empty() {
            return Ok(format!(
                "No tasks found with status: {}",
                status_filter.unwrap_or("any")
            ));
        }

        let tz = self.config.brain.timezone_offset;
        let mut response = String::new();
        for task in &tasks {
            let due_str = match task.due_at {
                Some(ts) => format!(" (due: {})", format_due(ts, tz)),
                None => String::new(),
            };
            response.push_str(&format!("- [{}] {}{}\n", task.status, task.title, due_str));
        }

        Ok(response.trim_end().to_string())
    }

    /// Handle task status updates. Finds task by title substring or #short_id and updates status.
    async fn handle_task_update(
        &self,
        title_query: &str,
        new_status: &str,
    ) -> Result<String> {
        let matching_tasks = self.find_tasks_smart(title_query).await?;

        if matching_tasks.is_empty() {
            return Ok(format!(
                "No tasks found matching \"{title_query}\". Check the title and try again."
            ));
        }

        if matching_tasks.len() > 1 {
            let mut response = format!(
                "Found {} tasks matching \"{title_query}\". Please be more specific:\n",
                matching_tasks.len()
            );
            for task in &matching_tasks {
                let short = &task.id[task.id.len().saturating_sub(6)..];
                response.push_str(&format!("- [{}] #{} {}\n", task.status, short, task.title));
            }
            return Ok(response.trim_end().to_string());
        }

        // Exactly one match — update it
        let task = &matching_tasks[0];
        self.tasks.update_task_status(&task.id, new_status).await?;

        Ok(format!(
            "Updated \"{}\" from {} to **{}**",
            task.title, task.status, new_status
        ))
    }

    /// Handle task deletion. Finds task by title substring and deletes it.
    /// If title_query is "*", deletes all tasks.
    async fn handle_task_delete(&self, title_query: &str) -> Result<String> {
        // Delete all tasks
        if title_query.trim() == "*" {
            let count = self.tasks.delete_all_tasks().await?;
            return Ok(format!("Deleted all tasks ({count} total)."));
        }

        if title_query.is_empty() {
            return Ok("Please specify which task to delete.".to_string());
        }

        let matching_tasks = self.find_tasks_smart(title_query).await?;

        if matching_tasks.is_empty() {
            return Ok(format!(
                "No tasks found matching \"{title_query}\". Check the title and try again."
            ));
        }

        if matching_tasks.len() > 1 {
            let mut response = format!(
                "Found {} tasks matching \"{title_query}\". Please be more specific:\n",
                matching_tasks.len()
            );
            for task in &matching_tasks {
                let short = &task.id[task.id.len().saturating_sub(6)..];
                response.push_str(&format!("- [{}] #{} {}\n", task.status, short, task.title));
            }
            return Ok(response.trim_end().to_string());
        }

        let task = &matching_tasks[0];
        self.tasks.delete_task(&task.id).await?;

        Ok(format!("Deleted task: \"{}\"", task.title))
    }

    /// Smart task lookup: tries #short_id, bare hex short_id, then title substring.
    async fn find_tasks_smart(&self, query: &str) -> Result<Vec<Task>> {
        // Explicit #short_id
        if let Some(short) = query.strip_prefix('#') {
            return self.tasks.find_task_by_short_id(short).await;
        }
        // Bare hex string (4-8 chars, all hex) — try as short_id first
        if query.len() >= 4 && query.len() <= 8 && query.chars().all(|c| c.is_ascii_hexdigit()) {
            let by_id = self.tasks.find_task_by_short_id(query).await?;
            if !by_id.is_empty() {
                return Ok(by_id);
            }
        }
        // Fallback: title substring
        self.tasks.find_task_by_title(query).await
    }

    /// Handle ingestion of text content. Chunks the text, embeds each chunk,
    /// and stores everything in the vector database.
    async fn handle_ingest(
        &self,
        content: &str,
        _conversation_id: &str,
    ) -> Result<String> {
        let (document, chunks) =
            self.pipeline
                .ingest_text(content, "message", None, None)?;

        // Store the document
        self.store.insert_document(&document).await?;

        // Embed and store each chunk
        let chunk_texts: Vec<&str> = chunks.iter().map(|(c, _)| c.content.as_str()).collect();

        if !chunk_texts.is_empty() {
            let embeddings = self.embed_text(&chunk_texts).await?;

            for ((chunk, _idx), embedding) in chunks.iter().zip(embeddings.iter()) {
                self.store.insert_chunk(chunk, embedding).await?;
            }
        }

        Ok(format!(
            "Got it! Saved and indexed {} chunk(s) to my knowledge base.",
            chunks.len()
        ))
    }

    // ─── Schedule handlers ────────────────────────────────────────────

    async fn handle_schedule_create(
        &self,
        description: &str,
        time: &str,
        recurrence: &str,
        day: Option<&str>,
        tools: &serde_json::Value,
        synthesis_prompt: Option<&str>,
    ) -> Result<String> {
        let schedule = match recurrence {
            "weekly" => {
                let d = day.unwrap_or("monday");
                format!("{time} weekly({d})")
            }
            "monthly" => {
                let d = day.unwrap_or("1");
                format!("{time} monthly({d})")
            }
            _ => format!("{time} daily"),
        };

        let now = now_unix();
        let tz = self.config.brain.timezone_offset;
        let next_run = compute_next_run(&schedule, now, tz)
            .ok_or_else(|| OasisError::Config("invalid schedule format".to_string()))?;

        let action = ScheduledAction {
            id: new_id(),
            description: description.to_string(),
            schedule: schedule.clone(),
            tool_calls: tools.to_string(),
            synthesis_prompt: synthesis_prompt.map(|s| s.to_string()),
            enabled: true,
            last_run: None,
            next_run,
            created_at: now,
        };

        self.store.insert_scheduled_action(&action).await?;

        let next_run_str = format_due(next_run, tz);
        Ok(format!(
            "Scheduled: {description}\nSchedule: {schedule}\nNext run: {next_run_str}"
        ))
    }

    async fn handle_schedule_list(&self) -> Result<String> {
        let actions = self.store.list_scheduled_actions().await?;
        if actions.is_empty() {
            return Ok("No scheduled actions.".to_string());
        }

        let tz = self.config.brain.timezone_offset;
        let mut output = format!("{} scheduled action(s):\n\n", actions.len());
        for (i, a) in actions.iter().enumerate() {
            let status = if a.enabled { "active" } else { "paused" };
            let next = format_due(a.next_run, tz);
            output.push_str(&format!(
                "{}. {} [{}]\n   Schedule: {} | Next: {}\n",
                i + 1,
                a.description,
                status,
                a.schedule,
                next,
            ));
        }
        Ok(output)
    }

    async fn handle_schedule_update(
        &self,
        query: &str,
        enabled: Option<bool>,
        time: Option<&str>,
        recurrence: Option<&str>,
        day: Option<&str>,
    ) -> Result<String> {
        let matches = self.store.find_scheduled_action_by_description(query).await?;
        if matches.is_empty() {
            return Ok(format!("No scheduled action matching \"{query}\"."));
        }
        if matches.len() > 1 {
            let names: Vec<_> = matches.iter().map(|a| a.description.as_str()).collect();
            return Ok(format!(
                "Multiple matches: {}. Be more specific.",
                names.join(", ")
            ));
        }

        let action = &matches[0];
        let tz = self.config.brain.timezone_offset;
        let mut changes = Vec::new();

        if let Some(en) = enabled {
            self.store.update_scheduled_action_enabled(&action.id, en).await?;
            changes.push(if en { "enabled" } else { "paused" });
        }

        if time.is_some() || recurrence.is_some() {
            // Rebuild schedule string
            let current_parts: Vec<&str> = action.schedule.splitn(2, ' ').collect();
            let new_time = time.unwrap_or(current_parts.first().copied().unwrap_or("08:00"));
            let new_rec = recurrence
                .map(|r| match r {
                    "weekly" => {
                        let d = day.unwrap_or("monday");
                        format!("weekly({d})")
                    }
                    "monthly" => {
                        let d = day.unwrap_or("1");
                        format!("monthly({d})")
                    }
                    _ => "daily".to_string(),
                })
                .unwrap_or_else(|| current_parts.get(1).copied().unwrap_or("daily").to_string());

            let new_schedule = format!("{new_time} {new_rec}");
            let now = now_unix();
            let next_run = compute_next_run(&new_schedule, now, tz)
                .ok_or_else(|| OasisError::Config("invalid schedule".to_string()))?;
            self.store.update_scheduled_action_schedule(&action.id, &new_schedule, next_run).await?;
            changes.push("schedule updated");
        }

        if changes.is_empty() {
            return Ok("No changes specified.".to_string());
        }

        Ok(format!(
            "Updated \"{}\": {}",
            action.description,
            changes.join(", ")
        ))
    }

    async fn handle_schedule_delete(&self, query: &str) -> Result<String> {
        if query == "*" {
            let count = self.store.delete_all_scheduled_actions().await?;
            return Ok(format!("Deleted all {count} scheduled action(s)."));
        }

        let matches = self.store.find_scheduled_action_by_description(query).await?;
        if matches.is_empty() {
            return Ok(format!("No scheduled action matching \"{query}\"."));
        }

        let mut deleted = 0;
        for a in &matches {
            self.store.delete_scheduled_action(&a.id).await?;
            deleted += 1;
        }

        if deleted == 1 {
            Ok(format!("Deleted: {}", matches[0].description))
        } else {
            Ok(format!("Deleted {deleted} scheduled action(s)."))
        }
    }

    // ─── Tool execution ──────────────────────────────────────────────

    /// Handle an action intent: run the tool execution loop.
    ///
    /// Sends user message + tool definitions to the LLM. If the LLM returns
    /// tool calls, executes them and feeds results back. Loops until the LLM
    /// produces a final text response (max 5 iterations).
    async fn handle_action(
        &self,
        chat_id: i64,
        text: &str,
        conversation_id: &str,
    ) -> Result<String> {
        let tools = builtin_tool_definitions();
        let task_summary = self
            .tasks
            .get_active_task_summary(self.config.brain.timezone_offset)
            .await?;

        let memory_context = match self.memory.build_memory_context().await {
            Ok(mc) => mc,
            Err(e) => {
                log!(" [memory] failed to load: {e}");
                String::new()
            }
        };

        let recent = self
            .store
            .get_recent_messages(conversation_id, self.config.brain.context_window)
            .await?;

        let mut messages = self.build_system_prompt(&task_summary, &memory_context, &recent);
        messages.push(ChatMessage::text("user", text));

        // Send placeholder
        let msg_id = self.bot.send_message_with_id(chat_id, "...").await?;

        const MAX_ITERATIONS: usize = 5;
        let mut final_text = String::new();

        for iteration in 0..MAX_ITERATIONS {
            log!(" [action] iteration {}/{MAX_ITERATIONS}", iteration + 1);

            let request = ChatRequest {
                messages: messages.clone(),
                max_tokens: Some(4096),
                temperature: Some(0.7),
            };

            let response = match Self::chat_with_tools_dispatch(&self.config, request, &tools).await {
                Ok(r) => r,
                Err(e) => {
                    log!(" [action] LLM error: {e}");
                    final_text = "Sorry, something went wrong. Please try again.".to_string();
                    break;
                }
            };

            if response.tool_calls.is_empty() {
                final_text = response.content;
                break;
            }

            // Add assistant's message (may contain both text and tool calls)
            let mut assistant_msg =
                ChatMessage::assistant_tool_calls(response.tool_calls.clone());
            if !response.content.is_empty() {
                assistant_msg.content = response.content.clone();
            }
            messages.push(assistant_msg);

            // Execute each tool call
            let mut last_output = String::new();
            for tool_call in &response.tool_calls {
                log!(" [tool] {}({})", tool_call.name, tool_call.arguments);
                let _ = self
                    .bot
                    .edit_message(
                        chat_id,
                        msg_id,
                        &format!("Using {}...", tool_call.name),
                    )
                    .await;

                let result = self
                    .execute_tool(&tool_call.name, &tool_call.arguments)
                    .await;
                log!(" [tool] {} → {} chars", tool_call.name, result.output.len());
                last_output = result.output.clone();

                messages.push(ChatMessage::tool_result(&tool_call.id, &result.output));
            }

            // Short-circuit for single simple tools — their output is already
            // human-readable, so we skip the extra LLM round-trip.
            if response.tool_calls.len() == 1 && !last_output.starts_with("Error:") {
                let is_simple = matches!(
                    response.tool_calls[0].name.as_str(),
                    "task_create" | "task_list" | "task_update" | "task_delete" | "remember"
                    | "schedule_create" | "schedule_list" | "schedule_update" | "schedule_delete"
                );
                if is_simple {
                    log!(" [action] short-circuit: simple tool, skipping LLM synthesis");
                    final_text = last_output;
                    break;
                }
            }
        }

        // If we exhausted iterations without a final text, force one without tools
        if final_text.is_empty() {
            log!(" [action] forcing final response (max iterations reached)");
            let request = ChatRequest {
                messages: messages.clone(),
                max_tokens: Some(4096),
                temperature: Some(0.7),
            };
            match Self::chat_with_tools_dispatch(&self.config, request, &[]).await {
                Ok(r) => final_text = r.content,
                Err(e) => {
                    log!(" [action] final LLM error: {e}");
                    final_text = "Sorry, something went wrong. Please try again.".to_string();
                }
            }
        }

        if final_text.is_empty() {
            final_text = "Done.".to_string();
        }

        let _ = self
            .bot
            .edit_message_formatted(chat_id, msg_id, &final_text)
            .await;
        log!(" [send] {} chars (action)", final_text.len());

        Ok(final_text)
    }

    /// Execute a single tool by name, dispatching to the appropriate handler.
    async fn execute_tool(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        match name {
            "task_create" => {
                let title = args["title"].as_str().unwrap_or("");
                let description = args["description"].as_str();
                let due = args["due"].as_str();
                let priority = priority_to_int(args["priority"].as_str());
                match self
                    .handle_task_create(title, description, due, priority)
                    .await
                {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "task_list" => {
                let status = args["status"].as_str();
                match self.handle_task_query(status).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "task_update" => {
                let title_query = args["title_query"].as_str().unwrap_or("");
                let new_status = args["new_status"].as_str().unwrap_or("done");
                match self.handle_task_update(title_query, new_status).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "task_delete" => {
                let title_query = args["title_query"].as_str().unwrap_or("");
                match self.handle_task_delete(title_query).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "web_search" => {
                let query = args["query"].as_str().unwrap_or("");
                match self.execute_web_search(query).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "knowledge_search" => {
                let query = args["query"].as_str().unwrap_or("");
                match self.execute_knowledge_search(query).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "remember" => {
                let content = args["content"].as_str().unwrap_or("");
                match self.handle_ingest(content, "").await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "schedule_create" => {
                let description = args["description"].as_str().unwrap_or("");
                let time = args["time"].as_str().unwrap_or("08:00");
                let recurrence = args["recurrence"].as_str().unwrap_or("daily");
                let day = args["day"].as_str();
                let tools = &args["tools"];
                let synthesis_prompt = args["synthesis_prompt"].as_str();
                match self.handle_schedule_create(description, time, recurrence, day, tools, synthesis_prompt).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "schedule_list" => {
                match self.handle_schedule_list().await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "schedule_update" => {
                let query = args["description_query"].as_str().unwrap_or("");
                let enabled = args["enabled"].as_bool();
                let time = args["time"].as_str();
                let recurrence = args["recurrence"].as_str();
                let day = args["day"].as_str();
                match self.handle_schedule_update(query, enabled, time, recurrence, day).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "schedule_delete" => {
                let query = args["description_query"].as_str().unwrap_or("");
                match self.handle_schedule_delete(query).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            _ => ToolResult::err(format!("Unknown tool: {name}")),
        }
    }

    /// Execute a web search and return ranked results as text (for tool use).
    ///
    /// If the top chunk's cosine similarity score is below the threshold,
    /// retries with an expanded result set and re-ranks the combined pool.
    async fn execute_web_search(&self, query: &str) -> Result<String> {
        const MIN_GOOD_SCORE: f32 = 0.35;

        // First pass: search 8 results
        let results = self.search.search(query, 8).await?;
        if results.is_empty() {
            return Ok(format!("No results found for \"{query}\"."));
        }

        let mut all_results = self.search.fetch_and_extract(results).await;
        let ranked = self.rank_search_results(query, &all_results).await;
        let top_score = ranked.first().map(|c| c.score).unwrap_or(0.0);

        // If relevance is too low, expand the search and re-rank
        if top_score < MIN_GOOD_SCORE {
            log!(
                " [search] top score {top_score:.3} < {MIN_GOOD_SCORE}, retrying with more results..."
            );
            let more = self.search.search(query, 12).await?;
            let more_with_content = self.search.fetch_and_extract(more).await;

            // Deduplicate by URL
            for r in more_with_content {
                if !all_results
                    .iter()
                    .any(|existing| existing.result.url == r.result.url)
                {
                    all_results.push(r);
                }
            }

            let ranked = self.rank_search_results(query, &all_results).await;
            return Ok(Self::format_ranked_results(&ranked, &all_results));
        }

        Ok(Self::format_ranked_results(&ranked, &all_results))
    }

    /// Format ranked chunks into a text response for the LLM.
    fn format_ranked_results(
        ranked: &[RankedChunk],
        results: &[oasis_search::search::SearchResultWithContent],
    ) -> String {
        let mut output = String::new();
        let mut seen_sources: Vec<usize> = Vec::new();

        for (i, chunk) in ranked.iter().enumerate().take(8) {
            output.push_str(&format!(
                "[{}] (score: {:.2}) {}\n{}\n\n",
                i + 1,
                chunk.score,
                chunk.source_title,
                chunk.text
            ));
            if !seen_sources.contains(&chunk.source_index) {
                seen_sources.push(chunk.source_index);
            }
        }

        output.push_str("Sources:\n");
        for idx in &seen_sources {
            if let Some(r) = results.get(*idx) {
                output.push_str(&format!("- {} ({})\n", r.result.title, r.result.url));
            }
        }

        output
    }

    /// Execute a knowledge search and return results as text (for tool use).
    async fn execute_knowledge_search(&self, query: &str) -> Result<String> {
        let query_embedding = self.embed_text(&[query]).await?;
        let embedding = query_embedding.first().ok_or_else(|| {
            OasisError::Embedding("no embedding returned".to_string())
        })?;

        let chunks = self
            .store
            .vector_search_chunks(embedding, self.config.brain.vector_top_k)
            .await?;
        let relevant_messages = self.store.vector_search_messages(embedding, 5).await?;

        let mut output = String::new();
        if !chunks.is_empty() {
            output.push_str("From knowledge base:\n");
            for (i, chunk) in chunks.iter().enumerate() {
                output.push_str(&format!("{}. {}\n\n", i + 1, chunk.content));
            }
        }
        if !relevant_messages.is_empty() {
            output.push_str("From past conversations:\n");
            for msg in &relevant_messages {
                output.push_str(&format!("[{}]: {}\n", msg.role, msg.content));
            }
        }
        if output.is_empty() {
            output = format!("No relevant information found for \"{query}\".");
        }

        Ok(output)
    }

    // ─── Chat and streaming ───────────────────────────────────────────

    /// Handle chat with streaming: send an initial placeholder message,
    /// then edit it as tokens arrive from the LLM.
    ///
    /// The `context` parameter allows injecting RAG context (from knowledge base
    /// search) into the system prompt. Pass empty string for regular chat.
    async fn handle_chat_stream(
        &self,
        chat_id: i64,
        message: &str,
        recent_messages: &[Message],
        context: &str,
    ) -> Result<String> {
        let task_summary = self.tasks.get_active_task_summary(self.config.brain.timezone_offset).await?;

        // Inject user memory into context
        let memory_context = match self.memory.build_memory_context().await {
            Ok(mc) => mc,
            Err(e) => {
                log!(" [memory] failed to load memory context: {e}");
                String::new()
            }
        };
        let full_context = if memory_context.is_empty() {
            context.to_string()
        } else if context.is_empty() {
            memory_context
        } else {
            format!("{memory_context}\n{context}")
        };

        let messages = self.build_system_prompt(&task_summary, &full_context, recent_messages);

        let mut all_messages = messages;
        all_messages.push(ChatMessage::text("user", message));

        let request = ChatRequest {
            messages: all_messages,
            max_tokens: Some(4096),
            temperature: Some(0.7),
        };

        let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel::<String>();

        // Spawn the LLM stream call
        let llm_handle = {
            let config = self.config.clone();
            tokio::spawn(async move {
                Self::chat_stream_dispatch(&config, request, tx).await
            })
        };

        // Send initial placeholder
        let msg_id = self.bot.send_message_with_id(chat_id, "Thinking...").await?;
        let mut accumulated = String::new();
        let mut last_edit = std::time::Instant::now();
        let edit_interval = std::time::Duration::from_secs(1);

        // Receive chunks and batch-edit the Telegram message
        while let Some(chunk) = rx.recv().await {
            accumulated.push_str(&chunk);

            // Edit at most once per interval to avoid Telegram rate limits
            if last_edit.elapsed() >= edit_interval {
                let _ = self.bot.edit_message(chat_id, msg_id, &accumulated).await;
                last_edit = std::time::Instant::now();
            }
        }

        // Final edit with Markdown formatting (complete text, so markdown should be valid)
        if !accumulated.is_empty() {
            let _ = self.bot.edit_message_formatted(chat_id, msg_id, &accumulated).await;
        }
        log!(" [send] {} chars (streamed)", accumulated.len());

        // Retrieve the final response (for usage logging)
        match llm_handle.await {
            Ok(Ok(resp)) => {
                if let Some(ref u) = resp.usage {
                    log!(" [llm] tokens: in={} out={}", u.input_tokens, u.output_tokens);
                }
            }
            Ok(Err(e)) => {
                log!(" [llm] stream error: {e}");
                if accumulated.is_empty() {
                    let _ = self.bot.edit_message(chat_id, msg_id, "Sorry, something went wrong. Please try again.").await;
                    return Err(e);
                }
            }
            Err(e) => {
                log!(" [llm] stream task panicked: {e}");
            }
        }

        Ok(accumulated)
    }

    /// Static dispatcher for streaming LLM calls (used from spawned task).
    async fn chat_stream_dispatch(
        config: &Config,
        request: ChatRequest,
        tx: tokio::sync::mpsc::UnboundedSender<String>,
    ) -> Result<ChatResponse> {
        match config.llm.provider.as_str() {
            "gemini" => {
                let provider = GeminiLlm::new(
                    config.llm.api_key.clone(),
                    config.llm.model.clone(),
                );
                provider.chat_stream(request, tx).await
            }
            "anthropic" => {
                let provider = AnthropicLlm::new(
                    config.llm.api_key.clone(),
                    config.llm.model.clone(),
                );
                provider.chat_stream(request, tx).await
            }
            "openai" => {
                let provider = OpenAiLlm::new(
                    config.llm.api_key.clone(),
                    config.llm.model.clone(),
                );
                provider.chat_stream(request, tx).await
            }
            "ollama" => {
                let provider = OllamaLlm::new(
                    config.ollama.base_url.clone(),
                    config.llm.model.clone(),
                );
                provider.chat_stream(request, tx).await
            }
            other => Err(OasisError::Config(format!(
                "unknown LLM provider: '{other}'"
            ))),
        }
    }

    /// Static dispatcher for chat_with_tools requests (used from tool execution loop).
    async fn chat_with_tools_dispatch(
        config: &Config,
        request: ChatRequest,
        tools: &[ToolDefinition],
    ) -> Result<ChatResponse> {
        match config.llm.provider.as_str() {
            "gemini" => {
                let provider = GeminiLlm::new(
                    config.llm.api_key.clone(),
                    config.llm.model.clone(),
                );
                provider.chat_with_tools(request, tools).await
            }
            "anthropic" => {
                let provider = AnthropicLlm::new(
                    config.llm.api_key.clone(),
                    config.llm.model.clone(),
                );
                provider.chat_with_tools(request, tools).await
            }
            "openai" => {
                let provider = OpenAiLlm::new(
                    config.llm.api_key.clone(),
                    config.llm.model.clone(),
                );
                provider.chat_with_tools(request, tools).await
            }
            "ollama" => {
                let provider = OllamaLlm::new(
                    config.ollama.base_url.clone(),
                    config.llm.model.clone(),
                );
                provider.chat_with_tools(request, tools).await
            }
            other => Err(OasisError::Config(format!(
                "unknown LLM provider: '{other}'"
            ))),
        }
    }

    // ─── File and URL ingestion ───────────────────────────────────────

    /// Handle a file upload from Telegram. Downloads the file, extracts text,
    /// and ingests it into the knowledge base.
    async fn handle_file(
        &self,
        doc: &TelegramDocument,
        _conversation_id: &str,
    ) -> Result<String> {
        // Get file info from Telegram
        let file_info = self.bot.get_file(&doc.file_id).await?;

        let file_path = file_info.file_path.ok_or_else(|| {
            OasisError::Telegram("file_path not returned by Telegram API".to_string())
        })?;

        // Download the file content
        let file_bytes = self.bot.download_file(&file_path).await?;

        // Convert to string (assuming text-based files)
        let content = String::from_utf8(file_bytes).map_err(|e| {
            OasisError::Ingest(format!("file is not valid UTF-8 text: {e}"))
        })?;

        let filename = doc
            .file_name
            .as_deref()
            .unwrap_or("unknown_file");

        // Use the ingest pipeline to process the file
        let (document, chunks) = self.pipeline.ingest_file(&content, filename)?;

        // Store the document
        self.store.insert_document(&document).await?;

        // Embed and store each chunk
        let chunk_texts: Vec<&str> = chunks.iter().map(|(c, _)| c.content.as_str()).collect();

        if !chunk_texts.is_empty() {
            let embeddings = self.embed_text(&chunk_texts).await?;

            for ((chunk, _idx), embedding) in chunks.iter().zip(embeddings.iter()) {
                self.store.insert_chunk(chunk, embedding).await?;
            }
        }

        Ok(format!(
            "File \"{}\" ingested: {} chunk(s) indexed.",
            filename,
            chunks.len()
        ))
    }

    /// Handle a URL message. Fetches the URL content, extracts text, and ingests it.
    async fn handle_url(
        &self,
        url: &str,
        _conversation_id: &str,
    ) -> Result<String> {
        // Fetch the URL content
        let html = IngestPipeline::fetch_url(url).await?;

        // Ingest as HTML
        let (document, chunks) = self.pipeline.ingest_html(&html, Some(url), None)?;

        // Store the document
        self.store.insert_document(&document).await?;

        // Embed and store each chunk
        let chunk_texts: Vec<&str> = chunks.iter().map(|(c, _)| c.content.as_str()).collect();

        if !chunk_texts.is_empty() {
            let embeddings = self.embed_text(&chunk_texts).await?;

            for ((chunk, _idx), embedding) in chunks.iter().zip(embeddings.iter()) {
                self.store.insert_chunk(chunk, embedding).await?;
            }
        }

        Ok(format!(
            "URL ingested: {} chunk(s) indexed from {}",
            chunks.len(),
            url
        ))
    }

    // ─── LLM and embedding dispatch ───────────────────────────────────

    /// Build the dynamic system prompt with task summary, knowledge context,
    /// and recent conversation history.
    fn build_system_prompt(
        &self,
        task_summary: &str,
        context: &str,
        recent: &[Message],
    ) -> Vec<ChatMessage> {
        let today = {
            let tz_offset = self.config.brain.timezone_offset;
            let (now_str, tz_str) = format_now_with_tz(tz_offset);
            format!("{now_str} (UTC{tz_str})")
        };

        let mut system = format!(
            "You are Oasis, a personal AI assistant. You are helpful, concise, and friendly.\n\
             Current date and time: {today}\n\n\
             ## Active tasks\n\
             {task_summary}\n"
        );

        if !context.is_empty() {
            system.push_str(&format!("\n{context}\n"));
        }

        // Embed conversation history in the system prompt so the model
        // clearly distinguishes context from the current user message.
        if !recent.is_empty() {
            system.push_str("\n## Recent conversation (for context only — respond to the user's NEW message, not these)\n");
            for msg in recent {
                let role_label = match msg.role.as_str() {
                    "user" => "User",
                    "assistant" => "Oasis",
                    _ => &msg.role,
                };
                system.push_str(&format!("{role_label}: {}\n", msg.content));
            }
        }

        vec![ChatMessage::text("system", system)]
    }

    // ─── Scheduled actions execution loop ───────────────────────────

    /// Background loop: check for and execute due scheduled actions every 60s.
    async fn run_scheduled_actions_loop(self: &Arc<Self>) {
        log!(" [sched] scheduled actions loop started");

        loop {
            tokio::time::sleep(std::time::Duration::from_secs(60)).await;

            if let Err(e) = self.check_and_run_scheduled_actions().await {
                log!(" [sched] error: {e}");
            }
        }
    }

    async fn check_and_run_scheduled_actions(self: &Arc<Self>) -> Result<()> {
        let now = now_unix();
        let due_actions = self.store.get_due_scheduled_actions(now).await?;

        if due_actions.is_empty() {
            return Ok(());
        }

        let chat_id = match self.store.get_config("owner_user_id").await? {
            Some(id_str) => match id_str.parse::<i64>() {
                Ok(id) => id,
                Err(_) => return Ok(()),
            },
            None => return Ok(()),
        };

        for action in &due_actions {
            log!(" [sched] executing: {}", action.description);

            let tool_calls: Vec<ScheduledToolCall> = match serde_json::from_str(&action.tool_calls) {
                Ok(tc) => tc,
                Err(e) => {
                    log!(" [sched] invalid tool_calls JSON: {e}");
                    continue;
                }
            };

            // Execute each tool and collect results
            let mut results = Vec::new();
            for tc in &tool_calls {
                log!(" [sched] tool: {}({})", tc.tool, tc.params);
                let result = self.execute_tool(&tc.tool, &tc.params).await;
                results.push(format!("## {}\n{}", tc.tool, result.output));
            }

            let combined = results.join("\n\n");

            // Synthesize with LLM if prompt provided, otherwise send raw
            let message = if let Some(ref prompt) = action.synthesis_prompt {
                self.synthesize_scheduled_result(&combined, prompt, &action.description).await
            } else {
                format!("**{}**\n\n{}", action.description, combined)
            };

            if let Err(e) = self.bot.send_message(chat_id, &message).await {
                log!(" [sched] send failed: {e}");
            }

            // Update last_run and compute next_run
            let tz = self.config.brain.timezone_offset;
            let next_run = compute_next_run(&action.schedule, now, tz)
                .unwrap_or(now + 86400);

            if let Err(e) = self.store.update_scheduled_action_run(&action.id, now, next_run).await {
                log!(" [sched] update run failed: {e}");
            }

            let next_str = format_due(next_run, tz);
            log!(" [sched] done: {}, next: {next_str}", action.description);
        }

        Ok(())
    }

    /// Synthesize scheduled action results using the intent LLM (Flash-Lite).
    async fn synthesize_scheduled_result(
        &self,
        tool_results: &str,
        synthesis_prompt: &str,
        description: &str,
    ) -> String {
        let tz_offset = self.config.brain.timezone_offset;
        let (now_str, tz_str) = format_now_with_tz(tz_offset);

        let system = format!(
            "You are Oasis, a personal AI assistant. Current time: {now_str} (UTC{tz_str}).\n\n\
             You are generating a scheduled report: \"{description}\".\n\
             User's formatting instruction: {synthesis_prompt}\n\n\
             Based on the tool results below, create a concise, well-formatted message.\n\n\
             Tool results:\n{tool_results}"
        );

        let request = ChatRequest {
            messages: vec![
                ChatMessage::text("system", system),
                ChatMessage::text("user", "Generate the report."),
            ],
            max_tokens: Some(2048),
            temperature: Some(0.5),
        };

        match self.chat_intent_llm(request).await {
            Ok(resp) => resp.content,
            Err(e) => {
                log!(" [sched] synthesis failed: {e}");
                format!("**{description}**\n\n{tool_results}")
            }
        }
    }

    /// Dispatch an embedding request to the configured provider.
    async fn embed_text(&self, texts: &[&str]) -> Result<Vec<Vec<f32>>> {
        let provider = &self.config.embedding.provider;
        let model = &self.config.embedding.model;
        log!(" [embed] calling {provider}/{model} for {} text(s)", texts.len());
        let result = self.embed_text_inner(texts).await;
        match &result {
            Ok(vecs) => {
                let dims = vecs.first().map(|v| v.len()).unwrap_or(0);
                log!(" [embed] OK, {} vector(s) x {dims} dims", vecs.len());
            }
            Err(e) => log!(" [embed] ERROR: {e}"),
        }
        result
    }

    async fn embed_text_inner(&self, texts: &[&str]) -> Result<Vec<Vec<f32>>> {
        match self.config.embedding.provider.as_str() {
            "openai" => {
                let provider = OpenAiEmbedding::new(
                    self.config.embedding.api_key.clone(),
                    self.config.embedding.model.clone(),
                    self.config.embedding.dimensions,
                );
                provider.embed(texts).await
            }
            "gemini" => {
                let provider = GeminiEmbedding::new(
                    self.config.embedding.api_key.clone(),
                    self.config.embedding.model.clone(),
                    self.config.embedding.dimensions,
                );
                provider.embed(texts).await
            }
            "ollama" => {
                let provider = OllamaEmbedding::new(
                    self.config.ollama.base_url.clone(),
                    self.config.embedding.model.clone(),
                    self.config.embedding.dimensions,
                );
                provider.embed(texts).await
            }
            other => Err(OasisError::Config(format!(
                "unknown embedding provider: '{other}'. Supported: openai, gemini, ollama"
            ))),
        }
    }

    /// Store a user message and an assistant response in the conversation.
    async fn store_message_pair(
        &self,
        conversation_id: &str,
        user_text: &str,
        assistant_text: &str,
    ) -> Result<()> {
        let now = now_unix();

        log!(" [store] saving message pair to conversation {conversation_id}");
        // Embed the user message for vector search
        let user_embedding = match self.embed_text(&[user_text]).await {
            Ok(embeddings) => embeddings.into_iter().next(),
            Err(e) => {
                log!(" [store] embed failed (non-fatal): {e}");
                None
            }
        };

        // Store user message
        let user_msg = Message {
            id: new_id(),
            conversation_id: conversation_id.to_string(),
            role: "user".to_string(),
            content: user_text.to_string(),
            created_at: now,
        };
        self.store
            .insert_message(&user_msg, user_embedding.as_deref())
            .await?;

        // Store assistant response (without embedding to save API calls)
        let assistant_msg = Message {
            id: new_id(),
            conversation_id: conversation_id.to_string(),
            role: "assistant".to_string(),
            content: assistant_text.to_string(),
            created_at: now,
        };
        self.store.insert_message(&assistant_msg, None).await?;

        Ok(())
    }
}

// ─── Helper functions ─────────────────────────────────────────────────

/// Check if an error is transient (worth retrying).
fn is_transient_error(err: &OasisError) -> bool {
    match err {
        OasisError::Http { status, .. } => matches!(status, 429 | 500 | 502 | 503 | 504),
        // Network errors from reqwest surface as Llm or Telegram errors with message text
        OasisError::Llm { message, .. } => {
            message.contains("timed out")
                || message.contains("connection")
                || message.contains("temporarily")
        }
        _ => false,
    }
}

/// Format a Unix timestamp (UTC) as a human-readable due date/time string in local time.
/// Shows "YYYY-MM-DD" if midnight local, or "YYYY-MM-DD HH:MM" if a specific time.
fn format_due(ts: i64, tz_offset: i32) -> String {
    let local_ts = ts + (tz_offset as i64) * 3600;
    let days = local_ts / 86400;
    let remainder = local_ts % 86400;
    let (y, m, d) = unix_days_to_date(days);
    // Check if the original UTC time was at a day boundary in local time
    // (i.e., the user set a date-only due, which is stored as midnight UTC adjusted)
    if (ts + (tz_offset as i64) * 3600) % 86400 == 0 {
        format!("{y:04}-{m:02}-{d:02}")
    } else {
        let h = remainder / 3600;
        let min = (remainder % 3600) / 60;
        format!("{y:04}-{m:02}-{d:02} {h:02}:{min:02}")
    }
}

/// Format the current date+time in the user's timezone.
/// Returns (datetime_string, tz_label) e.g. ("2026-02-16T16:00", "+07:00").
fn format_now_with_tz(tz_offset: i32) -> (String, String) {
    let utc_secs = now_unix();
    let local_secs = utc_secs + (tz_offset as i64) * 3600;
    let days = local_secs / 86400;
    let remainder = local_secs % 86400;
    let (y, m, d) = unix_days_to_date(days);
    let h = remainder / 3600;
    let min = (remainder % 3600) / 60;

    let datetime = format!("{y:04}-{m:02}-{d:02}T{h:02}:{min:02}");
    let tz_label = if tz_offset >= 0 {
        format!("+{:02}:00", tz_offset)
    } else {
        format!("-{:02}:00", tz_offset.unsigned_abs())
    };

    (datetime, tz_label)
}

/// Convert a priority string from the LLM to an integer.
fn priority_to_int(priority: Option<&str>) -> i32 {
    match priority {
        Some("low") => 1,
        Some("medium") => 2,
        Some("high") => 3,
        _ => 0,
    }
}

/// Parse a date string (in user's local time) to a Unix timestamp (UTC).
/// Accepts `YYYY-MM-DD` (start of day local) or `YYYY-MM-DDTHH:MM` (specific local time).
/// The `tz_offset` is the user's UTC offset in hours (e.g., 7 for UTC+7).
fn parse_date_to_unix(date_str: &str, tz_offset: i32) -> Option<i64> {
    let offset_secs = (tz_offset as i64) * 3600;

    // Try YYYY-MM-DDTHH:MM first
    if let Some((date_part, time_part)) = date_str.split_once('T') {
        let date_parts: Vec<&str> = date_part.split('-').collect();
        let time_parts: Vec<&str> = time_part.split(':').collect();
        if date_parts.len() == 3 && time_parts.len() >= 2 {
            let year: i64 = date_parts[0].parse().ok()?;
            let month: i64 = date_parts[1].parse().ok()?;
            let day: i64 = date_parts[2].parse().ok()?;
            let hour: i64 = time_parts[0].parse().ok()?;
            let minute: i64 = time_parts[1].parse().ok()?;
            if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 {
                return None;
            }
            let local_ts = date_to_unix_days(year, month, day) * 86400 + hour * 3600 + minute * 60;
            return Some(local_ts - offset_secs);
        }
    }

    // Fallback: YYYY-MM-DD (start of day in local time → UTC)
    let parts: Vec<&str> = date_str.split('-').collect();
    if parts.len() != 3 {
        return None;
    }

    let year: i64 = parts[0].parse().ok()?;
    let month: i64 = parts[1].parse().ok()?;
    let day: i64 = parts[2].parse().ok()?;

    if month < 1 || month > 12 || day < 1 || day > 31 {
        return None;
    }

    Some(date_to_unix_days(year, month, day) * 86400 - offset_secs)
}

/// Convert a (year, month, day) to days since Unix epoch.
/// Algorithm adapted from Howard Hinnant's days_from_civil.
fn date_to_unix_days(year: i64, month: i64, day: i64) -> i64 {
    let y = if month <= 2 { year - 1 } else { year };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = (y - era * 400) as u64; // year of era [0, 399]
    let m = month;
    let doy = (153 * (if m > 2 { m - 3 } else { m + 9 }) + 2) / 5 + day - 1; // day of year [0, 365]
    let doe = yoe as i64 * 365 + yoe as i64 / 4 - yoe as i64 / 100 + doy; // day of era [0, 146096]
    era * 146097 + doe - 719468
}

/// Convert a count of days since Unix epoch to (year, month, day).
/// Algorithm adapted from Howard Hinnant's civil_from_days.
fn unix_days_to_date(days: i64) -> (i64, i64, i64) {
    let z = days + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = (z - era * 146097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    (y, m as i64, d as i64)
}

/// Compute the next run time (UTC) for a schedule string.
///
/// Format: "HH:MM daily", "HH:MM weekly(day)", "HH:MM monthly(dom)"
/// `now` is current UTC unix timestamp, `tz_offset` is hours offset from UTC.
fn compute_next_run(schedule: &str, now: i64, tz_offset: i32) -> Option<i64> {
    let parts: Vec<&str> = schedule.splitn(2, ' ').collect();
    if parts.len() != 2 {
        return None;
    }

    let time_parts: Vec<&str> = parts[0].split(':').collect();
    if time_parts.len() != 2 {
        return None;
    }
    let hour: i64 = time_parts[0].parse().ok()?;
    let minute: i64 = time_parts[1].parse().ok()?;
    if hour > 23 || minute > 59 {
        return None;
    }

    let offset_secs = (tz_offset as i64) * 3600;
    let local_now = now + offset_secs;
    let local_days = local_now / 86400;
    let local_time_of_day = local_now % 86400;
    let target_time_of_day = hour * 3600 + minute * 60;

    let recurrence = parts[1].trim();

    match recurrence {
        "daily" => {
            let target_day = if local_time_of_day >= target_time_of_day {
                local_days + 1
            } else {
                local_days
            };
            let local_ts = target_day * 86400 + target_time_of_day;
            Some(local_ts - offset_secs)
        }
        s if s.starts_with("weekly(") => {
            let day_name = s.trim_start_matches("weekly(").trim_end_matches(')');
            let target_dow = day_name_to_dow(day_name)?;
            // Unix epoch (1970-01-01) was Thursday. local_days % 7: 0=Thu,1=Fri,...
            // Convert to Monday=0: (local_days + 3) % 7
            let current_dow = ((local_days % 7) + 3) % 7;
            let mut days_ahead = target_dow - current_dow;
            if days_ahead < 0 {
                days_ahead += 7;
            }
            if days_ahead == 0 && local_time_of_day >= target_time_of_day {
                days_ahead = 7;
            }
            let target_day = local_days + days_ahead;
            let local_ts = target_day * 86400 + target_time_of_day;
            Some(local_ts - offset_secs)
        }
        s if s.starts_with("monthly(") => {
            let dom_str = s.trim_start_matches("monthly(").trim_end_matches(')');
            let target_dom: i64 = dom_str.parse().ok()?;
            if target_dom < 1 || target_dom > 31 {
                return None;
            }
            let (y, m, d) = unix_days_to_date(local_days);
            let (target_y, target_m) =
                if d > target_dom || (d == target_dom && local_time_of_day >= target_time_of_day) {
                    if m == 12 {
                        (y + 1, 1)
                    } else {
                        (y, m + 1)
                    }
                } else {
                    (y, m)
                };
            let target_days = date_to_unix_days(target_y, target_m, target_dom);
            let local_ts = target_days * 86400 + target_time_of_day;
            Some(local_ts - offset_secs)
        }
        _ => None,
    }
}

/// Map a day name (English or Indonesian) to day-of-week (Monday=0 .. Sunday=6).
fn day_name_to_dow(name: &str) -> Option<i64> {
    match name.to_lowercase().as_str() {
        "monday" | "mon" | "senin" => Some(0),
        "tuesday" | "tue" | "selasa" => Some(1),
        "wednesday" | "wed" | "rabu" => Some(2),
        "thursday" | "thu" | "kamis" => Some(3),
        "friday" | "fri" | "jumat" => Some(4),
        "saturday" | "sat" | "sabtu" => Some(5),
        "sunday" | "sun" | "minggu" => Some(6),
        _ => None,
    }
}
