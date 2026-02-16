use oasis_core::error::{OasisError, Result};
use oasis_core::types::{ChatRequest, ChatResponse, Usage};
use reqwest::Client;
use serde_json::json;

use crate::provider::{EmbeddingProvider, LlmProvider};

/// Ollama local LLM chat provider.
pub struct OllamaLlm {
    client: Client,
    base_url: String,
    model: String,
}

impl OllamaLlm {
    /// Create a new Ollama LLM provider.
    ///
    /// # Arguments
    /// * `base_url` - Ollama server URL (e.g. "http://localhost:11434")
    /// * `model` - Model identifier (e.g. "llama3")
    pub fn new(base_url: String, model: String) -> Self {
        Self {
            client: Client::new(),
            base_url,
            model,
        }
    }
}

impl LlmProvider for OllamaLlm {
    async fn chat(&self, request: ChatRequest) -> Result<ChatResponse> {
        let url = format!("{}/api/chat", self.base_url);

        let messages: Vec<serde_json::Value> = request
            .messages
            .iter()
            .map(|m| {
                json!({
                    "role": m.role,
                    "content": m.content,
                })
            })
            .collect();

        let mut body = json!({
            "model": self.model,
            "messages": messages,
            "stream": false,
        });

        if let Some(temp) = request.temperature {
            body.as_object_mut().unwrap().insert(
                "options".to_string(),
                json!({ "temperature": temp }),
            );
        }

        let response = self
            .client
            .post(&url)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Llm {
                provider: "ollama".to_string(),
                message: format!("request failed: {e}"),
            })?;

        let status = response.status().as_u16();
        let response_text = response.text().await.map_err(|e| OasisError::Llm {
            provider: "ollama".to_string(),
            message: format!("failed to read response body: {e}"),
        })?;

        if status < 200 || status >= 300 {
            return Err(OasisError::Http {
                status,
                body: response_text,
            });
        }

        let parsed: serde_json::Value =
            serde_json::from_str(&response_text).map_err(|e| OasisError::Llm {
                provider: "ollama".to_string(),
                message: format!("failed to parse response JSON: {e}"),
            })?;

        let content = parsed["message"]["content"]
            .as_str()
            .ok_or_else(|| OasisError::Llm {
                provider: "ollama".to_string(),
                message: "missing message.content in response".to_string(),
            })?
            .to_string();

        let usage = match (
            parsed["prompt_eval_count"].as_u64(),
            parsed["eval_count"].as_u64(),
        ) {
            (Some(input), Some(output)) => Some(Usage {
                input_tokens: input as u32,
                output_tokens: output as u32,
            }),
            _ => None,
        };

        Ok(ChatResponse { content, tool_calls: vec![], usage })
    }

    fn name(&self) -> &str {
        "ollama"
    }
}

/// Ollama local embedding provider.
pub struct OllamaEmbedding {
    client: Client,
    base_url: String,
    model: String,
    dims: usize,
}

impl OllamaEmbedding {
    /// Create a new Ollama embedding provider.
    ///
    /// # Arguments
    /// * `base_url` - Ollama server URL (e.g. "http://localhost:11434")
    /// * `model` - Embedding model identifier (e.g. "nomic-embed-text")
    /// * `dims` - Expected embedding dimensionality (e.g. 768)
    pub fn new(base_url: String, model: String, dims: usize) -> Self {
        Self {
            client: Client::new(),
            base_url,
            model,
            dims,
        }
    }
}

impl EmbeddingProvider for OllamaEmbedding {
    async fn embed(&self, texts: &[&str]) -> Result<Vec<Vec<f32>>> {
        let url = format!("{}/api/embed", self.base_url);

        let body = json!({
            "model": self.model,
            "input": texts,
        });

        let response = self
            .client
            .post(&url)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Embedding(format!("ollama request failed: {e}")))?;

        let status = response.status().as_u16();
        let response_text = response.text().await.map_err(|e| {
            OasisError::Embedding(format!("ollama failed to read response body: {e}"))
        })?;

        if status < 200 || status >= 300 {
            return Err(OasisError::Http {
                status,
                body: response_text,
            });
        }

        let parsed: serde_json::Value = serde_json::from_str(&response_text).map_err(|e| {
            OasisError::Embedding(format!("ollama failed to parse response JSON: {e}"))
        })?;

        let embeddings_arr = parsed["embeddings"]
            .as_array()
            .ok_or_else(|| {
                OasisError::Embedding("missing embeddings array in ollama response".to_string())
            })?;

        let mut embeddings = Vec::with_capacity(embeddings_arr.len());
        for item in embeddings_arr {
            let embedding = item
                .as_array()
                .ok_or_else(|| {
                    OasisError::Embedding(
                        "expected array for embedding in ollama response".to_string(),
                    )
                })?
                .iter()
                .map(|v| {
                    v.as_f64()
                        .ok_or_else(|| {
                            OasisError::Embedding(
                                "non-numeric value in ollama embedding array".to_string(),
                            )
                        })
                        .map(|f| f as f32)
                })
                .collect::<Result<Vec<f32>>>()?;
            embeddings.push(embedding);
        }

        Ok(embeddings)
    }

    fn dimensions(&self) -> usize {
        self.dims
    }

    fn name(&self) -> &str {
        "ollama"
    }
}
