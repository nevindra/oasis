use libsql::{Builder, Connection, Database};
use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;

pub struct VectorStore {
    db: Database,
}

fn embedding_to_json(embedding: &[f32]) -> String {
    format!(
        "[{}]",
        embedding
            .iter()
            .map(|f| f.to_string())
            .collect::<Vec<_>>()
            .join(",")
    )
}

fn map_err(e: libsql::Error) -> OasisError {
    OasisError::Database(e.to_string())
}

const MAX_DB_RETRIES: u32 = 3;

fn is_transient_db_error(err: &OasisError) -> bool {
    match err {
        OasisError::Database(msg) => {
            msg.contains("Bad Gateway")
                || msg.contains("Service Unavailable")
                || msg.contains("Gateway Timeout")
                || msg.contains("timed out")
                || msg.contains("connection")
                || msg.contains("STREAM_EXPIRED")
        }
        _ => false,
    }
}

/// Retry an async database operation with exponential backoff on transient errors.
async fn with_retry<F, Fut, T>(f: F) -> Result<T>
where
    F: Fn() -> Fut,
    Fut: std::future::Future<Output = Result<T>>,
{
    let mut last_err = None;
    for attempt in 0..=MAX_DB_RETRIES {
        if attempt > 0 {
            let delay = std::time::Duration::from_secs(1 << (attempt - 1));
            log!(" [db] retry {attempt}/{MAX_DB_RETRIES} in {}s", delay.as_secs());
            tokio::time::sleep(delay).await;
        }
        match f().await {
            Ok(val) => return Ok(val),
            Err(e) if is_transient_db_error(&e) && attempt < MAX_DB_RETRIES => {
                log!(" [db] transient error: {e}");
                last_err = Some(e);
            }
            Err(e) => return Err(e),
        }
    }
    Err(last_err.unwrap())
}

impl VectorStore {
    /// Open a local libsql database at the given file path.
    pub async fn new(path: &str) -> Result<Self> {
        let db = Builder::new_local(path)
            .build()
            .await
            .map_err(map_err)?;
        let store = Self { db };
        store.init_tables().await?;
        Ok(store)
    }

    /// Open a remote Turso database.
    pub async fn new_remote(url: &str, token: &str) -> Result<Self> {
        let db = Builder::new_remote(url.to_string(), token.to_string())
            .build()
            .await
            .map_err(map_err)?;
        let store = Self { db };
        store.init_tables().await?;
        Ok(store)
    }

    /// Get a fresh database connection. For remote databases this creates
    /// a new Hrana stream, avoiding STREAM_EXPIRED errors.
    fn conn(&self) -> Result<Connection> {
        self.db.connect().map_err(map_err)
    }

    /// Create all required tables and vector indexes.
    async fn init_tables(&self) -> Result<()> {
        let conn = self.conn()?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS documents (
                id TEXT PRIMARY KEY,
                source_type TEXT NOT NULL,
                source_ref TEXT,
                title TEXT,
                raw_content TEXT NOT NULL,
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL
            )",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS chunks (
                id TEXT PRIMARY KEY,
                document_id TEXT NOT NULL REFERENCES documents(id),
                content TEXT NOT NULL,
                embedding F32_BLOB(1536),
                chunk_index INTEGER NOT NULL,
                created_at INTEGER NOT NULL
            )",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE INDEX IF NOT EXISTS chunks_vector_idx ON chunks(libsql_vector_idx(embedding, 'metric=cosine', 'compress_neighbors=float8', 'max_neighbors=64'))",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS projects (
                id TEXT PRIMARY KEY,
                name TEXT NOT NULL,
                description TEXT,
                status TEXT NOT NULL DEFAULT 'active',
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL
            )",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS tasks (
                id TEXT PRIMARY KEY,
                project_id TEXT REFERENCES projects(id),
                parent_task_id TEXT REFERENCES tasks(id),
                title TEXT NOT NULL,
                description TEXT,
                status TEXT NOT NULL DEFAULT 'todo',
                priority INTEGER DEFAULT 0,
                due_at INTEGER,
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL
            )",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS conversations (
                id TEXT PRIMARY KEY,
                telegram_chat_id INTEGER NOT NULL,
                created_at INTEGER NOT NULL
            )",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS messages (
                id TEXT PRIMARY KEY,
                conversation_id TEXT NOT NULL REFERENCES conversations(id),
                role TEXT NOT NULL,
                content TEXT NOT NULL,
                embedding F32_BLOB(1536),
                created_at INTEGER NOT NULL
            )",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE INDEX IF NOT EXISTS messages_vector_idx ON messages(libsql_vector_idx(embedding, 'metric=cosine'))",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS config (
                key TEXT PRIMARY KEY,
                value TEXT NOT NULL
            )",
            (),
        )
        .await
        .map_err(map_err)?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS scheduled_actions (
                id TEXT PRIMARY KEY,
                description TEXT NOT NULL,
                schedule TEXT NOT NULL,
                tool_calls TEXT NOT NULL,
                synthesis_prompt TEXT,
                enabled INTEGER DEFAULT 1,
                last_run INTEGER,
                next_run INTEGER NOT NULL,
                created_at INTEGER NOT NULL
            )",
            (),
        )
        .await
        .map_err(map_err)?;

        Ok(())
    }

    /// Insert a document record.
    pub async fn insert_document(&self, doc: &Document) -> Result<()> {
        with_retry(|| async {
            self.conn()?
                .execute(
                    "INSERT INTO documents (id, source_type, source_ref, title, raw_content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
                    libsql::params![
                        doc.id.clone(),
                        doc.source_type.clone(),
                        doc.source_ref.clone(),
                        doc.title.clone(),
                        doc.raw_content.clone(),
                        doc.created_at,
                        doc.updated_at,
                    ],
                )
                .await
                .map_err(map_err)?;
            Ok(())
        })
        .await
    }

    /// Insert a chunk with its embedding vector.
    pub async fn insert_chunk(&self, chunk: &Chunk, embedding: &[f32]) -> Result<()> {
        let embedding_json = embedding_to_json(embedding);
        with_retry(|| async {
            self.conn()?
                .execute(
                    "INSERT INTO chunks (id, document_id, content, embedding, chunk_index, created_at) VALUES (?, ?, ?, vector(?), ?, ?)",
                    libsql::params![
                        chunk.id.clone(),
                        chunk.document_id.clone(),
                        chunk.content.clone(),
                        embedding_json.clone(),
                        chunk.chunk_index,
                        chunk.created_at,
                    ],
                )
                .await
                .map_err(map_err)?;
            Ok(())
        })
        .await
    }

    /// Search chunks by vector similarity, returning the top_k most similar chunks.
    pub async fn vector_search_chunks(
        &self,
        embedding: &[f32],
        top_k: usize,
    ) -> Result<Vec<Chunk>> {
        let embedding_json = embedding_to_json(embedding);
        let mut rows = self
            .conn()?
            .query(
                "SELECT c.id, c.document_id, c.content, c.chunk_index, c.created_at FROM chunks c WHERE rowid IN vector_top_k('chunks_vector_idx', vector(?), ?)",
                libsql::params![embedding_json, top_k as i32],
            )
            .await
            .map_err(map_err)?;

        let mut chunks = Vec::new();
        while let Some(row) = rows.next().await.map_err(map_err)? {
            chunks.push(Chunk {
                id: row.get::<String>(0).map_err(map_err)?,
                document_id: row.get::<String>(1).map_err(map_err)?,
                content: row.get::<String>(2).map_err(map_err)?,
                chunk_index: row.get::<i32>(3).map_err(map_err)?,
                created_at: row.get::<i64>(4).map_err(map_err)?,
            });
        }
        Ok(chunks)
    }

    /// Insert a conversation record.
    pub async fn insert_conversation(&self, conv: &Conversation) -> Result<()> {
        self.conn()?
            .execute(
                "INSERT INTO conversations (id, telegram_chat_id, created_at) VALUES (?, ?, ?)",
                libsql::params![conv.id.clone(), conv.telegram_chat_id, conv.created_at],
            )
            .await
            .map_err(map_err)?;
        Ok(())
    }

    /// Get an existing conversation by telegram_chat_id, or create a new one if none exists.
    pub async fn get_or_create_conversation(
        &self,
        telegram_chat_id: i64,
    ) -> Result<Conversation> {
        let mut rows = self
            .conn()?
            .query(
                "SELECT id, telegram_chat_id, created_at FROM conversations WHERE telegram_chat_id = ? ORDER BY created_at DESC LIMIT 1",
                libsql::params![telegram_chat_id],
            )
            .await
            .map_err(map_err)?;

        if let Some(row) = rows.next().await.map_err(map_err)? {
            return Ok(Conversation {
                id: row.get::<String>(0).map_err(map_err)?,
                telegram_chat_id: row.get::<i64>(1).map_err(map_err)?,
                created_at: row.get::<i64>(2).map_err(map_err)?,
            });
        }

        self.create_new_conversation(telegram_chat_id).await
    }

    /// Create a new conversation for the given chat_id, regardless of whether one already exists.
    pub async fn create_new_conversation(
        &self,
        telegram_chat_id: i64,
    ) -> Result<Conversation> {
        let conv = Conversation {
            id: new_id(),
            telegram_chat_id,
            created_at: now_unix(),
        };
        self.insert_conversation(&conv).await?;
        Ok(conv)
    }

    /// Insert a message, optionally with an embedding vector.
    pub async fn insert_message(
        &self,
        msg: &Message,
        embedding: Option<&[f32]>,
    ) -> Result<()> {
        with_retry(|| async {
            let conn = self.conn()?;
            match embedding {
                Some(emb) => {
                    let embedding_json = embedding_to_json(emb);
                    conn.execute(
                        "INSERT INTO messages (id, conversation_id, role, content, embedding, created_at) VALUES (?, ?, ?, ?, vector(?), ?)",
                        libsql::params![
                            msg.id.clone(),
                            msg.conversation_id.clone(),
                            msg.role.clone(),
                            msg.content.clone(),
                            embedding_json,
                            msg.created_at,
                        ],
                    )
                    .await
                    .map_err(map_err)?;
                }
                None => {
                    conn.execute(
                        "INSERT INTO messages (id, conversation_id, role, content, embedding, created_at) VALUES (?, ?, ?, ?, NULL, ?)",
                        libsql::params![
                            msg.id.clone(),
                            msg.conversation_id.clone(),
                            msg.role.clone(),
                            msg.content.clone(),
                            msg.created_at,
                        ],
                    )
                    .await
                    .map_err(map_err)?;
                }
            }
            Ok(())
        })
        .await
    }

    /// Get the most recent messages for a conversation, returned in chronological order.
    pub async fn get_recent_messages(
        &self,
        conversation_id: &str,
        limit: usize,
    ) -> Result<Vec<Message>> {
        let mut rows = self
            .conn()?
            .query(
                "SELECT id, conversation_id, role, content, created_at FROM messages WHERE conversation_id = ? ORDER BY created_at DESC LIMIT ?",
                libsql::params![conversation_id.to_string(), limit as i32],
            )
            .await
            .map_err(map_err)?;

        let mut messages = Vec::new();
        while let Some(row) = rows.next().await.map_err(map_err)? {
            messages.push(Message {
                id: row.get::<String>(0).map_err(map_err)?,
                conversation_id: row.get::<String>(1).map_err(map_err)?,
                role: row.get::<String>(2).map_err(map_err)?,
                content: row.get::<String>(3).map_err(map_err)?,
                created_at: row.get::<i64>(4).map_err(map_err)?,
            });
        }
        messages.reverse();
        Ok(messages)
    }

    /// Search messages by vector similarity, returning the top_k most similar messages.
    pub async fn vector_search_messages(
        &self,
        embedding: &[f32],
        top_k: usize,
    ) -> Result<Vec<Message>> {
        let embedding_json = embedding_to_json(embedding);
        let mut rows = self
            .conn()?
            .query(
                "SELECT m.id, m.conversation_id, m.role, m.content, m.created_at FROM messages m WHERE rowid IN vector_top_k('messages_vector_idx', vector(?), ?)",
                libsql::params![embedding_json, top_k as i32],
            )
            .await
            .map_err(map_err)?;

        let mut messages = Vec::new();
        while let Some(row) = rows.next().await.map_err(map_err)? {
            messages.push(Message {
                id: row.get::<String>(0).map_err(map_err)?,
                conversation_id: row.get::<String>(1).map_err(map_err)?,
                role: row.get::<String>(2).map_err(map_err)?,
                content: row.get::<String>(3).map_err(map_err)?,
                created_at: row.get::<i64>(4).map_err(map_err)?,
            });
        }
        Ok(messages)
    }

    /// Set a configuration value (insert or replace).
    pub async fn set_config(&self, key: &str, value: &str) -> Result<()> {
        with_retry(|| async {
            self.conn()?
                .execute(
                    "INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)",
                    libsql::params![key.to_string(), value.to_string()],
                )
                .await
                .map_err(map_err)?;
            Ok(())
        })
        .await
    }

    /// Get a configuration value by key.
    pub async fn get_config(&self, key: &str) -> Result<Option<String>> {
        let mut rows = self
            .conn()?
            .query(
                "SELECT value FROM config WHERE key = ?",
                libsql::params![key.to_string()],
            )
            .await
            .map_err(map_err)?;

        if let Some(row) = rows.next().await.map_err(map_err)? {
            let value = row.get::<String>(0).map_err(map_err)?;
            Ok(Some(value))
        } else {
            Ok(None)
        }
    }

    // ─── Scheduled Actions ───────────────────────────────────────────

    /// Insert a new scheduled action.
    pub async fn insert_scheduled_action(&self, action: &ScheduledAction) -> Result<()> {
        self.conn()?
            .execute(
                "INSERT INTO scheduled_actions (id, description, schedule, tool_calls, synthesis_prompt, enabled, last_run, next_run, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                libsql::params![
                    action.id.clone(),
                    action.description.clone(),
                    action.schedule.clone(),
                    action.tool_calls.clone(),
                    action.synthesis_prompt.clone(),
                    action.enabled as i64,
                    action.last_run,
                    action.next_run,
                    action.created_at,
                ],
            )
            .await
            .map_err(map_err)?;
        Ok(())
    }

    /// List all scheduled actions.
    pub async fn list_scheduled_actions(&self) -> Result<Vec<ScheduledAction>> {
        let mut rows = self
            .conn()?
            .query("SELECT id, description, schedule, tool_calls, synthesis_prompt, enabled, last_run, next_run, created_at FROM scheduled_actions ORDER BY created_at DESC", ())
            .await
            .map_err(map_err)?;

        let mut actions = Vec::new();
        while let Some(row) = rows.next().await.map_err(map_err)? {
            actions.push(ScheduledAction {
                id: row.get::<String>(0).map_err(map_err)?,
                description: row.get::<String>(1).map_err(map_err)?,
                schedule: row.get::<String>(2).map_err(map_err)?,
                tool_calls: row.get::<String>(3).map_err(map_err)?,
                synthesis_prompt: row.get::<Option<String>>(4).map_err(map_err)?,
                enabled: row.get::<i64>(5).map_err(map_err)? != 0,
                last_run: row.get::<Option<i64>>(6).map_err(map_err)?,
                next_run: row.get::<i64>(7).map_err(map_err)?,
                created_at: row.get::<i64>(8).map_err(map_err)?,
            });
        }
        Ok(actions)
    }

    /// Find scheduled actions by description substring (case-insensitive).
    pub async fn find_scheduled_action_by_description(&self, query: &str) -> Result<Vec<ScheduledAction>> {
        let pattern = format!("%{query}%");
        let mut rows = self
            .conn()?
            .query(
                "SELECT id, description, schedule, tool_calls, synthesis_prompt, enabled, last_run, next_run, created_at FROM scheduled_actions WHERE LOWER(description) LIKE LOWER(?)",
                libsql::params![pattern],
            )
            .await
            .map_err(map_err)?;

        let mut actions = Vec::new();
        while let Some(row) = rows.next().await.map_err(map_err)? {
            actions.push(ScheduledAction {
                id: row.get::<String>(0).map_err(map_err)?,
                description: row.get::<String>(1).map_err(map_err)?,
                schedule: row.get::<String>(2).map_err(map_err)?,
                tool_calls: row.get::<String>(3).map_err(map_err)?,
                synthesis_prompt: row.get::<Option<String>>(4).map_err(map_err)?,
                enabled: row.get::<i64>(5).map_err(map_err)? != 0,
                last_run: row.get::<Option<i64>>(6).map_err(map_err)?,
                next_run: row.get::<i64>(7).map_err(map_err)?,
                created_at: row.get::<i64>(8).map_err(map_err)?,
            });
        }
        Ok(actions)
    }

    /// Get scheduled actions that are due for execution.
    pub async fn get_due_scheduled_actions(&self, now: i64) -> Result<Vec<ScheduledAction>> {
        let mut rows = self
            .conn()?
            .query(
                "SELECT id, description, schedule, tool_calls, synthesis_prompt, enabled, last_run, next_run, created_at FROM scheduled_actions WHERE enabled = 1 AND next_run <= ? ORDER BY next_run ASC",
                libsql::params![now],
            )
            .await
            .map_err(map_err)?;

        let mut actions = Vec::new();
        while let Some(row) = rows.next().await.map_err(map_err)? {
            actions.push(ScheduledAction {
                id: row.get::<String>(0).map_err(map_err)?,
                description: row.get::<String>(1).map_err(map_err)?,
                schedule: row.get::<String>(2).map_err(map_err)?,
                tool_calls: row.get::<String>(3).map_err(map_err)?,
                synthesis_prompt: row.get::<Option<String>>(4).map_err(map_err)?,
                enabled: row.get::<i64>(5).map_err(map_err)? != 0,
                last_run: row.get::<Option<i64>>(6).map_err(map_err)?,
                next_run: row.get::<i64>(7).map_err(map_err)?,
                created_at: row.get::<i64>(8).map_err(map_err)?,
            });
        }
        Ok(actions)
    }

    /// Update last_run and next_run after execution.
    pub async fn update_scheduled_action_run(&self, id: &str, last_run: i64, next_run: i64) -> Result<()> {
        self.conn()?
            .execute(
                "UPDATE scheduled_actions SET last_run = ?, next_run = ? WHERE id = ?",
                libsql::params![last_run, next_run, id.to_string()],
            )
            .await
            .map_err(map_err)?;
        Ok(())
    }

    /// Enable or disable a scheduled action.
    pub async fn update_scheduled_action_enabled(&self, id: &str, enabled: bool) -> Result<()> {
        self.conn()?
            .execute(
                "UPDATE scheduled_actions SET enabled = ? WHERE id = ?",
                libsql::params![enabled as i64, id.to_string()],
            )
            .await
            .map_err(map_err)?;
        Ok(())
    }

    /// Update the schedule and recompute next_run.
    pub async fn update_scheduled_action_schedule(&self, id: &str, schedule: &str, next_run: i64) -> Result<()> {
        self.conn()?
            .execute(
                "UPDATE scheduled_actions SET schedule = ?, next_run = ? WHERE id = ?",
                libsql::params![schedule.to_string(), next_run, id.to_string()],
            )
            .await
            .map_err(map_err)?;
        Ok(())
    }

    /// Delete a scheduled action by ID.
    pub async fn delete_scheduled_action(&self, id: &str) -> Result<()> {
        self.conn()?
            .execute(
                "DELETE FROM scheduled_actions WHERE id = ?",
                libsql::params![id.to_string()],
            )
            .await
            .map_err(map_err)?;
        Ok(())
    }

    /// Delete all scheduled actions.
    pub async fn delete_all_scheduled_actions(&self) -> Result<u64> {
        let count = self
            .conn()?
            .execute("DELETE FROM scheduled_actions", ())
            .await
            .map_err(map_err)?;
        Ok(count)
    }
}
