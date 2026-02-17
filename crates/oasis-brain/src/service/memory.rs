use libsql::Database;
use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;
use serde::{Deserialize, Serialize};

fn db_err(e: libsql::Error) -> OasisError {
    OasisError::Database(e.to_string())
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

/// The prompt sent to the LLM to extract facts from a conversation turn.
pub const EXTRACT_FACTS_PROMPT: &str = r#"You are a memory extraction system. Given a conversation between a user and an assistant, extract factual information ABOUT THE USER.

Extract facts like:
- Personal info (name, job, location, timezone)
- Preferences (communication style, tools, languages)
- Habits and routines
- Current projects or goals
- Relationships and people they mention

Rules:
- Only extract facts clearly stated or strongly implied by the USER (not the assistant)
- Each fact should be a single, concise statement
- Categorize each fact as: personal, preference, work, habit, or relationship
- If a new fact CONTRADICTS or UPDATES a previously known fact, include a "supersedes" field with the old fact text
- If no new user facts are present, return an empty array
- Do NOT extract facts about the assistant or general knowledge

Return a JSON array:
[{"fact": "User moved to Bali", "category": "personal", "supersedes": "Lives in Jakarta"}]

If the fact does not supersede anything, omit the "supersedes" field:
[{"fact": "User's name is Nev", "category": "personal"}]

Return ONLY the JSON array, no extra text. Return [] if no facts found."#;

/// A user fact extracted from conversation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserFact {
    pub id: String,
    pub fact: String,
    pub category: String,
    pub confidence: f64,
    pub source_message_id: Option<String>,
    pub created_at: i64,
    pub updated_at: i64,
}

/// A parsed fact from LLM extraction.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExtractedFact {
    pub fact: String,
    pub category: String,
    #[serde(default)]
    pub supersedes: Option<String>,
}

/// Check whether a user message is worth running fact extraction on.
/// Skips very short messages and common filler replies.
pub fn should_extract_facts(text: &str) -> bool {
    let trimmed = text.trim();
    if trimmed.len() < 10 {
        return false;
    }
    let lower = trimmed.to_lowercase();
    let skip = [
        "ok", "oke", "okay", "okey",
        "thanks", "thank you", "makasih", "thx", "ty",
        "yes", "no", "ya", "ga", "gak", "nggak", "engga",
        "nice", "sip", "siap", "oke sip",
        "lol", "haha", "wkwk", "wkwkwk",
        "hmm", "hm", "oh", "ah",
        "good", "great", "cool", "yep", "nope",
    ];
    !skip.iter().any(|s| lower == *s)
}

/// Memory store for user facts and conversation topics.
///
/// The MemoryStore handles its own DB tables. It provides:
/// - `init()` — create tables
/// - `extract_facts()` — parse LLM output into facts
/// - `upsert_fact()` — insert or update a fact
/// - `get_relevant_facts()` — retrieve top facts for system prompt injection
/// - `delete_matching_facts()` — for "forget about X" commands
/// - `decay_old_facts()` — prune stale facts
pub struct MemoryStore {
    db: Database,
}

impl MemoryStore {
    pub fn new(db: Database) -> Self {
        Self { db }
    }

    fn conn(&self) -> Result<libsql::Connection> {
        self.db.connect().map_err(db_err)
    }

    /// Create the memory tables.
    pub async fn init(&self) -> Result<()> {
        let conn = self.conn()?;

        conn.execute(
            "CREATE TABLE IF NOT EXISTS user_facts (
                id TEXT PRIMARY KEY,
                fact TEXT NOT NULL,
                category TEXT NOT NULL,
                confidence REAL DEFAULT 1.0,
                embedding F32_BLOB(1536),
                source_message_id TEXT,
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL
            )",
            (),
        )
        .await
        .map_err(db_err)?;

        conn.execute(
            "CREATE INDEX IF NOT EXISTS user_facts_vector_idx ON user_facts(libsql_vector_idx(embedding, 'metric=cosine', 'compress_neighbors=float8', 'max_neighbors=64'))",
            (),
        )
        .await
        .map_err(db_err)?;

        Ok(())
    }

    /// Parse the LLM's fact extraction response into structured facts.
    pub fn parse_extracted_facts(llm_response: &str) -> Vec<ExtractedFact> {
        let trimmed = llm_response.trim();

        // Strip markdown code fences if present
        let json_str = if trimmed.starts_with("```") {
            let stripped = trimmed
                .strip_prefix("```json")
                .or_else(|| trimmed.strip_prefix("```"))
                .unwrap_or(trimmed)
                .strip_suffix("```")
                .unwrap_or(trimmed)
                .trim();
            stripped
        } else {
            trimmed
        };

        // Find the array brackets
        let start = match json_str.find('[') {
            Some(i) => i,
            None => return Vec::new(),
        };
        let end = match json_str.rfind(']') {
            Some(i) => i,
            None => return Vec::new(),
        };

        if end < start {
            return Vec::new();
        }

        let array_str = &json_str[start..=end];
        serde_json::from_str(array_str).unwrap_or_default()
    }

    /// Insert a new fact or merge with an existing semantically similar fact.
    ///
    /// If a fact with cosine similarity > 0.85 exists, replace it (merge & replace).
    /// Otherwise insert as new.
    pub async fn upsert_fact(
        &self,
        fact: &str,
        category: &str,
        embedding: &[f32],
        source_message_id: Option<&str>,
    ) -> Result<()> {
        let now = now_unix();
        let embedding_json = embedding_to_json(embedding);

        // Search for semantically similar existing facts
        let conn = self.conn()?;
        let mut rows = conn
            .query(
                "SELECT uf.id, uf.confidence, vector_distance_cos(uf.embedding, vector(?1)) as distance \
                 FROM user_facts uf \
                 WHERE uf.rowid IN vector_top_k('user_facts_vector_idx', vector(?1), 3)",
                libsql::params![embedding_json.clone()],
            )
            .await
            .map_err(db_err)?;

        // Find the closest fact within threshold
        let mut best_match: Option<(String, f64)> = None;
        while let Some(row) = rows.next().await.map_err(db_err)? {
            let id: String = row.get(0).map_err(db_err)?;
            let confidence: f64 = row.get(1).map_err(db_err)?;
            let distance: f64 = row.get(2).map_err(db_err)?;

            // cosine distance < 0.15 means similarity > 0.85
            if distance < 0.15 {
                best_match = Some((id, confidence));
                break;
            }
        }

        if let Some((id, confidence)) = best_match {
            // Similar fact exists — replace text, update embedding, bump confidence
            let new_confidence = (confidence + 0.1).min(1.0);
            self.conn()?
                .execute(
                    "UPDATE user_facts SET fact = ?1, category = ?2, embedding = vector(?3), \
                     confidence = ?4, updated_at = ?5 WHERE id = ?6",
                    libsql::params![
                        fact.to_string(),
                        category.to_string(),
                        embedding_json,
                        new_confidence,
                        now,
                        id
                    ],
                )
                .await
                .map_err(db_err)?;
        } else {
            // New fact
            let id = new_id();
            self.conn()?
                .execute(
                    "INSERT INTO user_facts (id, fact, category, confidence, embedding, \
                     source_message_id, created_at, updated_at) \
                     VALUES (?1, ?2, ?3, 1.0, vector(?4), ?5, ?6, ?7)",
                    libsql::params![
                        id,
                        fact.to_string(),
                        category.to_string(),
                        embedding_json,
                        source_message_id.map(|s| s.to_string()),
                        now,
                        now
                    ],
                )
                .await
                .map_err(db_err)?;
        }

        Ok(())
    }

    /// Get the top N most relevant facts, sorted by confidence and recency.
    pub async fn get_top_facts(&self, limit: usize) -> Result<Vec<UserFact>> {
        let mut facts = Vec::new();
        let conn = self.conn()?;

        let mut rows = conn
            .query(
                "SELECT id, fact, category, confidence, source_message_id, created_at, updated_at \
                 FROM user_facts \
                 WHERE confidence >= 0.3 \
                 ORDER BY confidence DESC, updated_at DESC \
                 LIMIT ?1",
                libsql::params![limit as i64],
            )
            .await
            .map_err(db_err)?;

        while let Some(row) = rows.next().await.map_err(db_err)? {
            let source_msg_id = {
                let val = row.get::<libsql::Value>(4).map_err(db_err)?;
                match val {
                    libsql::Value::Null => None,
                    libsql::Value::Text(s) => Some(s),
                    _ => None,
                }
            };

            facts.push(UserFact {
                id: row.get(0).map_err(db_err)?,
                fact: row.get(1).map_err(db_err)?,
                category: row.get(2).map_err(db_err)?,
                confidence: row.get(3).map_err(db_err)?,
                source_message_id: source_msg_id,
                created_at: row.get(5).map_err(db_err)?,
                updated_at: row.get(6).map_err(db_err)?,
            });
        }

        Ok(facts)
    }

    /// Get facts most relevant to a query, using vector similarity.
    pub async fn get_relevant_facts(
        &self,
        query_embedding: &[f32],
        limit: usize,
    ) -> Result<Vec<UserFact>> {
        let embedding_json = embedding_to_json(query_embedding);
        let conn = self.conn()?;

        let mut rows = conn
            .query(
                "SELECT uf.id, uf.fact, uf.category, uf.confidence, uf.source_message_id, \
                 uf.created_at, uf.updated_at \
                 FROM user_facts uf \
                 WHERE uf.confidence >= 0.3 \
                 AND uf.rowid IN vector_top_k('user_facts_vector_idx', vector(?1), ?2)",
                libsql::params![embedding_json, limit as i64],
            )
            .await
            .map_err(db_err)?;

        let mut facts = Vec::new();
        while let Some(row) = rows.next().await.map_err(db_err)? {
            let source_msg_id = {
                let val = row.get::<libsql::Value>(4).map_err(db_err)?;
                match val {
                    libsql::Value::Null => None,
                    libsql::Value::Text(s) => Some(s),
                    _ => None,
                }
            };

            facts.push(UserFact {
                id: row.get(0).map_err(db_err)?,
                fact: row.get(1).map_err(db_err)?,
                category: row.get(2).map_err(db_err)?,
                confidence: row.get(3).map_err(db_err)?,
                source_message_id: source_msg_id,
                created_at: row.get(5).map_err(db_err)?,
                updated_at: row.get(6).map_err(db_err)?,
            });
        }

        Ok(facts)
    }

    /// Build a memory context block for injection into the system prompt.
    ///
    /// When a query embedding is provided, retrieves facts by relevance.
    /// Otherwise falls back to top facts by confidence/recency.
    pub async fn build_memory_context(
        &self,
        query_embedding: Option<&[f32]>,
    ) -> Result<String> {
        let facts = if let Some(emb) = query_embedding {
            self.get_relevant_facts(emb, 10).await?
        } else {
            self.get_top_facts(15).await?
        };

        if facts.is_empty() {
            return Ok(String::new());
        }

        let mut context = String::from("## What you know about the user\n");
        for fact in &facts {
            context.push_str(&format!("- {} [{}]\n", fact.fact, fact.category));
        }

        Ok(context)
    }

    /// Delete facts matching a query (for "forget about X" commands).
    pub async fn delete_matching_facts(&self, query: &str) -> Result<usize> {
        let pattern = format!("%{query}%");
        let affected = self
            .conn()?
            .execute(
                "DELETE FROM user_facts WHERE fact LIKE ?1",
                libsql::params![pattern],
            )
            .await
            .map_err(db_err)?;

        Ok(affected as usize)
    }

    /// Decay confidence of old facts that haven't been reinforced.
    /// Facts below threshold (0.3) and older than 30 days are pruned.
    pub async fn decay_old_facts(&self) -> Result<usize> {
        let now = now_unix();
        let thirty_days_ago = now - (30 * 86400);
        let conn = self.conn()?;

        // Decay confidence of facts not updated in 7+ days
        let seven_days_ago = now - (7 * 86400);
        conn.execute(
            "UPDATE user_facts SET confidence = confidence * 0.95 WHERE updated_at < ?1 AND confidence > 0.3",
            libsql::params![seven_days_ago],
        )
        .await
        .map_err(db_err)?;

        // Prune facts with very low confidence that are old
        let pruned = conn
            .execute(
                "DELETE FROM user_facts WHERE confidence < 0.3 AND updated_at < ?1",
                libsql::params![thirty_days_ago],
            )
            .await
            .map_err(db_err)?;

        Ok(pruned as usize)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_facts_basic() {
        let response = r#"[{"fact":"User's name is Nev","category":"personal"},{"fact":"Works as a software engineer","category":"work"}]"#;
        let facts = MemoryStore::parse_extracted_facts(response);
        assert_eq!(facts.len(), 2);
        assert_eq!(facts[0].fact, "User's name is Nev");
        assert_eq!(facts[0].category, "personal");
        assert_eq!(facts[1].fact, "Works as a software engineer");
        assert_eq!(facts[1].category, "work");
    }

    #[test]
    fn test_parse_facts_empty() {
        let response = "[]";
        let facts = MemoryStore::parse_extracted_facts(response);
        assert!(facts.is_empty());
    }

    #[test]
    fn test_parse_facts_code_fence() {
        let response = "```json\n[{\"fact\":\"Prefers Rust\",\"category\":\"preference\"}]\n```";
        let facts = MemoryStore::parse_extracted_facts(response);
        assert_eq!(facts.len(), 1);
        assert_eq!(facts[0].fact, "Prefers Rust");
    }

    #[test]
    fn test_parse_facts_surrounding_text() {
        let response = "Here are the facts:\n[{\"fact\":\"Lives in Jakarta\",\"category\":\"personal\"}]\nDone.";
        let facts = MemoryStore::parse_extracted_facts(response);
        assert_eq!(facts.len(), 1);
        assert_eq!(facts[0].fact, "Lives in Jakarta");
    }

    #[test]
    fn test_parse_facts_invalid_json() {
        let response = "This is not JSON at all";
        let facts = MemoryStore::parse_extracted_facts(response);
        assert!(facts.is_empty());
    }

    #[test]
    fn test_should_extract_trivial() {
        assert!(!should_extract_facts("ok"));
        assert!(!should_extract_facts("Oke"));
        assert!(!should_extract_facts("thanks"));
        assert!(!should_extract_facts("sip"));
        assert!(!should_extract_facts("lol"));
        assert!(!should_extract_facts("wkwk"));
        assert!(!should_extract_facts("ya"));
        assert!(!should_extract_facts("short")); // < 10 chars
    }

    #[test]
    fn test_should_extract_real_messages() {
        assert!(should_extract_facts("Gue tinggal di Jakarta sekarang"));
        assert!(should_extract_facts("I work as a software engineer"));
        assert!(should_extract_facts("My name is Nev and I like Rust"));
    }

    #[test]
    fn test_parse_facts_with_supersedes() {
        let response = r#"[{"fact":"User moved to Bali","category":"personal","supersedes":"Lives in Jakarta"}]"#;
        let facts = MemoryStore::parse_extracted_facts(response);
        assert_eq!(facts.len(), 1);
        assert_eq!(facts[0].fact, "User moved to Bali");
        assert_eq!(facts[0].supersedes, Some("Lives in Jakarta".to_string()));
    }

    #[test]
    fn test_parse_facts_without_supersedes() {
        let response = r#"[{"fact":"User's name is Nev","category":"personal"}]"#;
        let facts = MemoryStore::parse_extracted_facts(response);
        assert_eq!(facts.len(), 1);
        assert!(facts[0].supersedes.is_none());
    }
}
