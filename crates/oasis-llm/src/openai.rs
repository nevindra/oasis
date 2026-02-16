use oasis_core::error::{OasisError, Result};
use oasis_core::types::{ChatRequest, ChatResponse, Usage};
use reqwest::Client;
use serde_json::json;

use crate::provider::{EmbeddingProvider, LlmProvider};

const OPENAI_CHAT_URL: &str = "https://api.openai.com/v1/chat/completions";
const OPENAI_EMBEDDINGS_URL: &str = "https://api.openai.com/v1/embeddings";

/// OpenAI chat completion provider.
pub struct OpenAiLlm {
    client: Client,
    api_key: String,
    model: String,
}

impl OpenAiLlm {
    /// Create a new OpenAI LLM provider.
    ///
    /// # Arguments
    /// * `api_key` - OpenAI API key
    /// * `model` - Model identifier (e.g. "gpt-4o")
    pub fn new(api_key: String, model: String) -> Self {
        Self {
            client: Client::new(),
            api_key,
            model,
        }
    }
}

impl LlmProvider for OpenAiLlm {
    async fn chat(&self, request: ChatRequest) -> Result<ChatResponse> {
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
        });

        if let Some(max_tokens) = request.max_tokens {
            body.as_object_mut()
                .unwrap()
                .insert("max_tokens".to_string(), json!(max_tokens));
        }

        if let Some(temp) = request.temperature {
            body.as_object_mut()
                .unwrap()
                .insert("temperature".to_string(), json!(temp));
        }

        let response = self
            .client
            .post(OPENAI_CHAT_URL)
            .bearer_auth(&self.api_key)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Llm {
                provider: "openai".to_string(),
                message: format!("request failed: {e}"),
            })?;

        let status = response.status().as_u16();
        let response_text = response.text().await.map_err(|e| OasisError::Llm {
            provider: "openai".to_string(),
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
                provider: "openai".to_string(),
                message: format!("failed to parse response JSON: {e}"),
            })?;

        let content = parsed["choices"]
            .as_array()
            .and_then(|arr| arr.first())
            .and_then(|choice| choice["message"]["content"].as_str())
            .ok_or_else(|| OasisError::Llm {
                provider: "openai".to_string(),
                message: "missing choices[0].message.content in response".to_string(),
            })?
            .to_string();

        let usage = match (
            parsed["usage"]["prompt_tokens"].as_u64(),
            parsed["usage"]["completion_tokens"].as_u64(),
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
        "openai"
    }
}

/// OpenAI embedding provider.
pub struct OpenAiEmbedding {
    client: Client,
    api_key: String,
    model: String,
    dims: usize,
}

impl OpenAiEmbedding {
    /// Create a new OpenAI embedding provider.
    ///
    /// # Arguments
    /// * `api_key` - OpenAI API key
    /// * `model` - Embedding model identifier (e.g. "text-embedding-3-small")
    /// * `dims` - Expected embedding dimensionality (e.g. 1536)
    pub fn new(api_key: String, model: String, dims: usize) -> Self {
        Self {
            client: Client::new(),
            api_key,
            model,
            dims,
        }
    }
}

impl EmbeddingProvider for OpenAiEmbedding {
    async fn embed(&self, texts: &[&str]) -> Result<Vec<Vec<f32>>> {
        let body = json!({
            "model": self.model,
            "input": texts,
        });

        let response = self
            .client
            .post(OPENAI_EMBEDDINGS_URL)
            .bearer_auth(&self.api_key)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Embedding(format!("openai request failed: {e}")))?;

        let status = response.status().as_u16();
        let response_text = response.text().await.map_err(|e| {
            OasisError::Embedding(format!("openai failed to read response body: {e}"))
        })?;

        if status < 200 || status >= 300 {
            return Err(OasisError::Http {
                status,
                body: response_text,
            });
        }

        let parsed: serde_json::Value = serde_json::from_str(&response_text).map_err(|e| {
            OasisError::Embedding(format!("openai failed to parse response JSON: {e}"))
        })?;

        let data = parsed["data"]
            .as_array()
            .ok_or_else(|| OasisError::Embedding("missing data array in response".to_string()))?;

        let mut embeddings = Vec::with_capacity(data.len());
        for item in data {
            let embedding = item["embedding"]
                .as_array()
                .ok_or_else(|| {
                    OasisError::Embedding("missing embedding array in data item".to_string())
                })?
                .iter()
                .map(|v| {
                    v.as_f64()
                        .ok_or_else(|| {
                            OasisError::Embedding(
                                "non-numeric value in embedding array".to_string(),
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
        "openai"
    }
}
