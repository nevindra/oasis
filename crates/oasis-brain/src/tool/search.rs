use std::sync::Arc;

use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use serde_json::json;

use crate::service::ingest::chunker::{chunk_text, ChunkerConfig};
use crate::service::llm::Embedder;
use crate::service::search::{parse_element_ref, SearchResultWithContent, WebSearch};
use crate::tool::{Tool, ToolResult};

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

pub struct SearchTool {
    search: Arc<WebSearch>,
    embedder: Arc<Embedder>,
}

impl SearchTool {
    pub fn new(search: Arc<WebSearch>, embedder: Arc<Embedder>) -> Self {
        Self { search, embedder }
    }

    /// Close the active browser session, freeing resources.
    pub async fn close_browse_session(&self) {
        self.search.close_browse_session().await;
    }
}

#[async_trait]
impl Tool for SearchTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![
            ToolDefinition {
                name: "web_search".to_string(),
                description: "Search the web for current/real-time information. Use for recent events, news, prices, weather, or anything that requires up-to-date data.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "query": { "type": "string", "description": "Search query optimized for search engines" }
                    },
                    "required": ["query"]
                }),
            },
            ToolDefinition {
                name: "browse_url".to_string(),
                description: "Open a URL in a browser and return the page content with interactive elements. Use to interact with web pages, fill forms, check prices, etc.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "url": { "type": "string", "description": "The URL to navigate to" }
                    },
                    "required": ["url"]
                }),
            },
            ToolDefinition {
                name: "page_click".to_string(),
                description: "Click an interactive element on the current browser page by its number from the elements list. Use after browse_url.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "element": { "type": "string", "description": "Element number to click (e.g. '3' for element [3])" }
                    },
                    "required": ["element"]
                }),
            },
            ToolDefinition {
                name: "page_type".to_string(),
                description: "Type text into an input field on the current browser page by its element number. Replaces existing text. Use after browse_url.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "element": { "type": "string", "description": "Element number to type into (e.g. '1' for element [1])" },
                        "text": { "type": "string", "description": "Text to type into the element" }
                    },
                    "required": ["element", "text"]
                }),
            },
            ToolDefinition {
                name: "page_read".to_string(),
                description: "Read the current browser page content and interactive elements without any interaction. Use to refresh the view after waiting or to re-read the page.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {}
                }),
            },
        ]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        match name {
            "web_search" => {
                let query = args["query"].as_str().unwrap_or("");
                match self.execute_web_search(query).await {
                    Ok(r) => ToolResult::ok(r),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "browse_url" => {
                let url = args["url"].as_str().unwrap_or("");
                match self.search.browse_to(url).await {
                    Ok(snapshot) => ToolResult::ok(snapshot.to_llm_text()),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "page_click" => {
                let element = args["element"].as_str().unwrap_or("0");
                let idx = parse_element_ref(element);
                match self.search.page_click(idx).await {
                    Ok(snapshot) => ToolResult::ok(snapshot.to_llm_text()),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "page_type" => {
                let element = args["element"].as_str().unwrap_or("0");
                let text = args["text"].as_str().unwrap_or("");
                let idx = parse_element_ref(element);
                match self.search.page_type_into(idx, text).await {
                    Ok(snapshot) => ToolResult::ok(snapshot.to_llm_text()),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            "page_read" => {
                match self.search.read_page().await {
                    Ok(snapshot) => ToolResult::ok(snapshot.to_llm_text()),
                    Err(e) => ToolResult::err(e.to_string()),
                }
            }
            _ => ToolResult::err(format!("Unknown search tool: {name}")),
        }
    }
}

impl SearchTool {
    async fn execute_web_search(&self, query: &str) -> oasis_core::error::Result<String> {
        const MIN_GOOD_SCORE: f32 = 0.35;

        let results = self.search.search(query, 8).await?;
        if results.is_empty() {
            return Ok(format!("No results found for \"{query}\"."));
        }

        let mut all_results = self.search.fetch_and_extract(results).await;
        let ranked = self.rank_search_results(query, &all_results).await;
        let top_score = ranked.first().map(|c| c.score).unwrap_or(0.0);

        if top_score < MIN_GOOD_SCORE {
            log!(
                " [search] top score {top_score:.3} < {MIN_GOOD_SCORE}, retrying with more results..."
            );
            let more = self.search.search(query, 12).await?;
            let more_with_content = self.search.fetch_and_extract(more).await;

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

    async fn rank_search_results(
        &self,
        query: &str,
        results: &[SearchResultWithContent],
    ) -> Vec<RankedChunk> {
        let chunk_config = ChunkerConfig {
            max_chars: 500,
            overlap_chars: 0,
        };

        let mut tagged_chunks: Vec<RankedChunk> = Vec::new();
        for (i, r) in results.iter().enumerate() {
            if !r.result.snippet.is_empty() {
                tagged_chunks.push(RankedChunk {
                    text: r.result.snippet.clone(),
                    source_index: i,
                    source_title: r.result.title.clone(),
                    score: 0.0,
                });
            }

            if let Some(ref content) = r.content {
                let chunks = chunk_text(content, &chunk_config);
                for chunk_text in chunks {
                    if chunk_text.len() < 50 {
                        continue;
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

        let mut texts: Vec<&str> = vec![query];
        for chunk in &tagged_chunks {
            texts.push(&chunk.text);
        }

        let embeddings = match self.embedder.embed(&texts).await {
            Ok(e) => e,
            Err(e) => {
                log!(" [search] embedding failed: {e}, falling back to unranked");
                tagged_chunks.truncate(8);
                return tagged_chunks;
            }
        };

        let query_vec = &embeddings[0];
        for (i, chunk) in tagged_chunks.iter_mut().enumerate() {
            chunk.score = cosine_similarity(query_vec, &embeddings[i + 1]);
        }

        tagged_chunks.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });

        log!(
            " [search] top score: {:.3}, bottom: {:.3}",
            tagged_chunks.first().map(|c| c.score).unwrap_or(0.0),
            tagged_chunks.last().map(|c| c.score).unwrap_or(0.0),
        );

        tagged_chunks
    }

    fn format_ranked_results(
        ranked: &[RankedChunk],
        results: &[SearchResultWithContent],
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
}
