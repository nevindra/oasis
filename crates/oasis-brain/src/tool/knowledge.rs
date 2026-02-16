use std::sync::Arc;

use async_trait::async_trait;
use oasis_core::error::OasisError;
use oasis_core::types::ToolDefinition;
use serde_json::json;

use crate::service::llm::Embedder;
use crate::service::store::VectorStore;
use crate::tool::{Tool, ToolResult};

pub struct KnowledgeTool {
    store: Arc<VectorStore>,
    embedder: Arc<Embedder>,
    vector_top_k: usize,
}

impl KnowledgeTool {
    pub fn new(store: Arc<VectorStore>, embedder: Arc<Embedder>, vector_top_k: usize) -> Self {
        Self { store, embedder, vector_top_k }
    }
}

#[async_trait]
impl Tool for KnowledgeTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![ToolDefinition {
            name: "knowledge_search".to_string(),
            description: "Search the user's personal knowledge base for previously saved information, documents, and past conversations.".to_string(),
            parameters: json!({
                "type": "object",
                "properties": {
                    "query": { "type": "string", "description": "Search query" }
                },
                "required": ["query"]
            }),
        }]
    }

    async fn execute(&self, _name: &str, args: &serde_json::Value) -> ToolResult {
        let query = args["query"].as_str().unwrap_or("");
        match self.execute_search(query).await {
            Ok(r) => ToolResult::ok(r),
            Err(e) => ToolResult::err(e.to_string()),
        }
    }
}

impl KnowledgeTool {
    async fn execute_search(&self, query: &str) -> oasis_core::error::Result<String> {
        let query_embedding = self.embedder.embed(&[query]).await?;
        let embedding = query_embedding.first().ok_or_else(|| {
            OasisError::Embedding("no embedding returned".to_string())
        })?;

        let chunks = self
            .store
            .vector_search_chunks(embedding, self.vector_top_k)
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
}
