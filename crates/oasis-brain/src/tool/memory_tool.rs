use std::sync::Arc;

use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use serde_json::json;

use crate::service::ingest::pipeline::IngestPipeline;
use crate::service::llm::Embedder;
use crate::service::store::VectorStore;
use crate::tool::{Tool, ToolResult};

pub struct MemoryTool {
    pipeline: IngestPipeline,
    store: Arc<VectorStore>,
    embedder: Arc<Embedder>,
}

impl MemoryTool {
    pub fn new(
        pipeline: IngestPipeline,
        store: Arc<VectorStore>,
        embedder: Arc<Embedder>,
    ) -> Self {
        Self { pipeline, store, embedder }
    }

    /// Ingest text content into the knowledge base.
    pub async fn ingest_text(&self, content: &str) -> oasis_core::error::Result<String> {
        let (document, chunks) =
            self.pipeline
                .ingest_text(content, "message", None, None)?;

        self.store.insert_document(&document).await?;

        let chunk_texts: Vec<&str> = chunks.iter().map(|(c, _)| c.content.as_str()).collect();
        if !chunk_texts.is_empty() {
            let embeddings = self.embedder.embed(&chunk_texts).await?;
            for ((chunk, _idx), embedding) in chunks.iter().zip(embeddings.iter()) {
                self.store.insert_chunk(chunk, embedding).await?;
            }
        }

        Ok(format!(
            "Got it! Saved and indexed {} chunk(s) to my knowledge base.",
            chunks.len()
        ))
    }

    /// Ingest a file's content into the knowledge base.
    pub async fn ingest_file(&self, content: &str, filename: &str) -> oasis_core::error::Result<String> {
        let (document, chunks) = self.pipeline.ingest_file(content, filename)?;

        self.store.insert_document(&document).await?;

        let chunk_texts: Vec<&str> = chunks.iter().map(|(c, _)| c.content.as_str()).collect();
        if !chunk_texts.is_empty() {
            let embeddings = self.embedder.embed(&chunk_texts).await?;
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

    /// Ingest HTML content from a URL into the knowledge base.
    pub async fn ingest_url(&self, html: &str, url: &str) -> oasis_core::error::Result<String> {
        let (document, chunks) = self.pipeline.ingest_html(html, Some(url), None)?;

        self.store.insert_document(&document).await?;

        let chunk_texts: Vec<&str> = chunks.iter().map(|(c, _)| c.content.as_str()).collect();
        if !chunk_texts.is_empty() {
            let embeddings = self.embedder.embed(&chunk_texts).await?;
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
}

#[async_trait]
impl Tool for MemoryTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![ToolDefinition {
            name: "remember".to_string(),
            description: "Save information to the user's knowledge base. Use when the user explicitly asks to remember or save something.".to_string(),
            parameters: json!({
                "type": "object",
                "properties": {
                    "content": { "type": "string", "description": "The content to save" }
                },
                "required": ["content"]
            }),
        }]
    }

    async fn execute(&self, _name: &str, args: &serde_json::Value) -> ToolResult {
        let content = args["content"].as_str().unwrap_or("");
        match self.ingest_text(content).await {
            Ok(r) => ToolResult::ok(r),
            Err(e) => ToolResult::err(e.to_string()),
        }
    }
}
