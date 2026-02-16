use oasis_core::error::{OasisError, Result};
use oasis_core::types::{ChatRequest, ChatResponse, ToolCallRequest, ToolDefinition, Usage};
use reqwest::Client;
use serde_json::json;
use tokio::sync::mpsc;

use crate::provider::{EmbeddingProvider, LlmProvider};

const GEMINI_BASE_URL: &str = "https://generativelanguage.googleapis.com/v1beta";

/// Google Gemini chat completion provider.
pub struct GeminiLlm {
    client: Client,
    api_key: String,
    model: String,
}

impl GeminiLlm {
    /// Create a new Gemini LLM provider.
    ///
    /// # Arguments
    /// * `api_key` - Google API key
    /// * `model` - Model identifier (e.g. "gemini-2.0-flash")
    pub fn new(api_key: String, model: String) -> Self {
        Self {
            client: Client::new(),
            api_key,
            model,
        }
    }

    /// Map a standard role to the Gemini API role.
    /// "user" stays "user", "assistant" becomes "model".
    fn map_role(role: &str) -> &str {
        match role {
            "assistant" => "model",
            other => other,
        }
    }

    /// Build the JSON request body from a ChatRequest.
    fn build_body(request: &ChatRequest) -> serde_json::Value {
        Self::build_body_inner(request, &[])
    }

    /// Build the JSON request body with optional tool definitions.
    fn build_body_inner(request: &ChatRequest, tools: &[ToolDefinition]) -> serde_json::Value {
        let mut system_parts: Vec<String> = Vec::new();
        let mut contents: Vec<serde_json::Value> = Vec::new();

        for m in &request.messages {
            if m.role == "system" {
                system_parts.push(m.content.clone());
            } else if !m.tool_calls.is_empty() {
                // Assistant message with tool calls → model role with functionCall parts
                let parts: Vec<serde_json::Value> = m.tool_calls.iter().map(|tc| {
                    let mut part = json!({ "functionCall": { "name": tc.name, "args": tc.arguments } });
                    // Include thoughtSignature if present (required by Gemini for multi-turn)
                    if let Some(ref meta) = tc.metadata {
                        if let Some(sig) = meta.get("thoughtSignature") {
                            part.as_object_mut().unwrap().insert("thoughtSignature".to_string(), sig.clone());
                        }
                    }
                    part
                }).collect();
                contents.push(json!({ "role": "model", "parts": parts }));
            } else if m.role == "tool" {
                // Tool result message → user role with functionResponse part
                let tool_call_id = m.tool_call_id.as_deref().unwrap_or("");
                contents.push(json!({
                    "role": "user",
                    "parts": [{
                        "functionResponse": {
                            "name": tool_call_id,
                            "response": { "result": m.content }
                        }
                    }]
                }));
            } else {
                let mut parts: Vec<serde_json::Value> = Vec::new();
                if !m.content.is_empty() {
                    parts.push(json!({ "text": m.content }));
                }
                for img in &m.images {
                    parts.push(json!({
                        "inlineData": {
                            "mimeType": img.mime_type,
                            "data": img.base64,
                        }
                    }));
                }
                if parts.is_empty() {
                    parts.push(json!({ "text": "" }));
                }
                contents.push(json!({
                    "role": Self::map_role(&m.role),
                    "parts": parts,
                }));
            }
        }

        let mut body = json!({ "contents": contents });

        if !system_parts.is_empty() {
            let combined = system_parts.join("\n\n");
            body.as_object_mut().unwrap().insert(
                "systemInstruction".to_string(),
                json!({ "parts": [{ "text": combined }] }),
            );
        }

        // Add tool definitions
        if !tools.is_empty() {
            let declarations: Vec<serde_json::Value> = tools.iter().map(|t| {
                json!({
                    "name": t.name,
                    "description": t.description,
                    "parameters": t.parameters,
                })
            }).collect();
            body.as_object_mut().unwrap().insert(
                "tools".to_string(),
                json!([{ "functionDeclarations": declarations }]),
            );
        }

        if request.temperature.is_some() || request.max_tokens.is_some() {
            let mut gen = serde_json::Map::new();
            if let Some(temp) = request.temperature {
                gen.insert("temperature".to_string(), json!(temp));
            }
            if let Some(max_tokens) = request.max_tokens {
                gen.insert("maxOutputTokens".to_string(), json!(max_tokens));
            }
            body.as_object_mut()
                .unwrap()
                .insert("generationConfig".to_string(), json!(gen));
        }

        body
    }
}

impl LlmProvider for GeminiLlm {
    async fn chat(&self, request: ChatRequest) -> Result<ChatResponse> {
        let url = format!(
            "{}/models/{}:generateContent?key={}",
            GEMINI_BASE_URL, self.model, self.api_key
        );

        let body = Self::build_body(&request);

        let response = self
            .client
            .post(&url)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Llm {
                provider: "gemini".to_string(),
                message: format!("request failed: {e}"),
            })?;

        let status = response.status().as_u16();
        let response_text = response.text().await.map_err(|e| OasisError::Llm {
            provider: "gemini".to_string(),
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
                provider: "gemini".to_string(),
                message: format!("failed to parse response JSON: {e}"),
            })?;

        let content = parsed["candidates"]
            .as_array()
            .and_then(|arr| arr.first())
            .and_then(|candidate| candidate["content"]["parts"].as_array())
            .and_then(|parts| parts.first())
            .and_then(|part| part["text"].as_str())
            .ok_or_else(|| OasisError::Llm {
                provider: "gemini".to_string(),
                message: "missing candidates[0].content.parts[0].text in response".to_string(),
            })?
            .to_string();

        let usage = match (
            parsed["usageMetadata"]["promptTokenCount"].as_u64(),
            parsed["usageMetadata"]["candidatesTokenCount"].as_u64(),
        ) {
            (Some(input), Some(output)) => Some(Usage {
                input_tokens: input as u32,
                output_tokens: output as u32,
            }),
            _ => None,
        };

        Ok(ChatResponse { content, tool_calls: vec![], usage })
    }

    async fn chat_with_tools(
        &self,
        request: ChatRequest,
        tools: &[ToolDefinition],
    ) -> Result<ChatResponse> {
        let url = format!(
            "{}/models/{}:generateContent?key={}",
            GEMINI_BASE_URL, self.model, self.api_key
        );

        let body = Self::build_body_inner(&request, tools);

        let response = self
            .client
            .post(&url)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Llm {
                provider: "gemini".to_string(),
                message: format!("request failed: {e}"),
            })?;

        let status = response.status().as_u16();
        let response_text = response.text().await.map_err(|e| OasisError::Llm {
            provider: "gemini".to_string(),
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
                provider: "gemini".to_string(),
                message: format!("failed to parse response JSON: {e}"),
            })?;

        let parts = parsed["candidates"]
            .as_array()
            .and_then(|arr| arr.first())
            .and_then(|candidate| candidate["content"]["parts"].as_array());

        let mut content = String::new();
        let mut tool_calls = Vec::new();

        if let Some(parts) = parts {
            for part in parts {
                if let Some(text) = part["text"].as_str() {
                    content.push_str(text);
                }
                if let Some(fc) = part.get("functionCall") {
                    let name = fc["name"].as_str().unwrap_or("").to_string();
                    let args = fc["args"].clone();
                    // Capture thoughtSignature for multi-turn (Gemini requires it)
                    let metadata = part.get("thoughtSignature").map(|sig| {
                        json!({ "thoughtSignature": sig })
                    });
                    tool_calls.push(ToolCallRequest {
                        id: name.clone(), // Gemini uses function name as ID
                        name,
                        arguments: args,
                        metadata,
                    });
                }
            }
        }

        let usage = match (
            parsed["usageMetadata"]["promptTokenCount"].as_u64(),
            parsed["usageMetadata"]["candidatesTokenCount"].as_u64(),
        ) {
            (Some(input), Some(output)) => Some(Usage {
                input_tokens: input as u32,
                output_tokens: output as u32,
            }),
            _ => None,
        };

        Ok(ChatResponse { content, tool_calls, usage })
    }

    async fn chat_stream(
        &self,
        request: ChatRequest,
        tx: mpsc::UnboundedSender<String>,
    ) -> Result<ChatResponse> {
        use futures_util::StreamExt;

        let url = format!(
            "{}/models/{}:streamGenerateContent?key={}&alt=sse",
            GEMINI_BASE_URL, self.model, self.api_key
        );

        let body = Self::build_body(&request);

        let response = self
            .client
            .post(&url)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Llm {
                provider: "gemini".to_string(),
                message: format!("stream request failed: {e}"),
            })?;

        let status = response.status().as_u16();
        if status < 200 || status >= 300 {
            let body = response.text().await.unwrap_or_default();
            return Err(OasisError::Http { status, body });
        }

        let mut full_content = String::new();
        let mut usage: Option<Usage> = None;
        let mut stream = response.bytes_stream();
        let mut buffer = String::new();

        while let Some(chunk_result) = stream.next().await {
            let chunk = chunk_result.map_err(|e| OasisError::Llm {
                provider: "gemini".to_string(),
                message: format!("stream read error: {e}"),
            })?;

            buffer.push_str(&String::from_utf8_lossy(&chunk));

            // SSE format: lines starting with "data: " followed by JSON
            while let Some(data_start) = buffer.find("data: ") {
                let json_start = data_start + 6;
                // Find end of this SSE event (double newline or next "data: ")
                let json_end = buffer[json_start..]
                    .find("\n\ndata: ")
                    .or_else(|| buffer[json_start..].find("\r\n\r\n"))
                    .map(|pos| json_start + pos)
                    .unwrap_or(buffer.len());

                let json_str = buffer[json_start..json_end].trim();

                // Skip if incomplete JSON (no closing brace at nesting level 0)
                if json_str.is_empty() || !is_complete_json(json_str) {
                    break;
                }

                if let Ok(parsed) = serde_json::from_str::<serde_json::Value>(json_str) {
                    // Extract text chunk
                    if let Some(text) = parsed["candidates"]
                        .as_array()
                        .and_then(|arr| arr.first())
                        .and_then(|c| c["content"]["parts"].as_array())
                        .and_then(|parts| parts.first())
                        .and_then(|p| p["text"].as_str())
                    {
                        full_content.push_str(text);
                        let _ = tx.send(text.to_string());
                    }

                    // Extract usage from the last chunk
                    if let (Some(input), Some(output)) = (
                        parsed["usageMetadata"]["promptTokenCount"].as_u64(),
                        parsed["usageMetadata"]["candidatesTokenCount"].as_u64(),
                    ) {
                        usage = Some(Usage {
                            input_tokens: input as u32,
                            output_tokens: output as u32,
                        });
                    }
                }

                buffer = buffer[json_end..].to_string();
            }
        }

        Ok(ChatResponse {
            content: full_content,
            tool_calls: vec![],
            usage,
        })
    }

    fn name(&self) -> &str {
        "gemini"
    }
}

/// Quick check if a JSON string looks complete (balanced braces).
fn is_complete_json(s: &str) -> bool {
    let mut depth = 0i32;
    let mut in_string = false;
    let mut escape = false;

    for ch in s.chars() {
        if escape {
            escape = false;
            continue;
        }
        if ch == '\\' && in_string {
            escape = true;
            continue;
        }
        if ch == '"' {
            in_string = !in_string;
            continue;
        }
        if in_string {
            continue;
        }
        match ch {
            '{' | '[' => depth += 1,
            '}' | ']' => depth -= 1,
            _ => {}
        }
    }
    depth == 0 && !in_string
}

/// Google Gemini embedding provider.
pub struct GeminiEmbedding {
    client: Client,
    api_key: String,
    model: String,
    dims: usize,
}

impl GeminiEmbedding {
    /// Create a new Gemini embedding provider.
    ///
    /// # Arguments
    /// * `api_key` - Google API key
    /// * `model` - Embedding model identifier (e.g. "text-embedding-004")
    /// * `dims` - Expected embedding dimensionality (e.g. 768)
    pub fn new(api_key: String, model: String, dims: usize) -> Self {
        Self {
            client: Client::new(),
            api_key,
            model,
            dims,
        }
    }
}

impl EmbeddingProvider for GeminiEmbedding {
    async fn embed(&self, texts: &[&str]) -> Result<Vec<Vec<f32>>> {
        let url = format!(
            "{}/models/{}:embedContent?key={}",
            GEMINI_BASE_URL, self.model, self.api_key
        );

        let mut embeddings = Vec::with_capacity(texts.len());

        for text in texts {
            let body = json!({
                "content": {
                    "parts": [{ "text": text }]
                },
                "outputDimensionality": self.dims
            });

            let response = self
                .client
                .post(&url)
                .header("content-type", "application/json")
                .json(&body)
                .send()
                .await
                .map_err(|e| {
                    OasisError::Embedding(format!("gemini request failed: {e}"))
                })?;

            let status = response.status().as_u16();
            let response_text = response.text().await.map_err(|e| {
                OasisError::Embedding(format!("gemini failed to read response body: {e}"))
            })?;

            if status < 200 || status >= 300 {
                return Err(OasisError::Http {
                    status,
                    body: response_text,
                });
            }

            let parsed: serde_json::Value =
                serde_json::from_str(&response_text).map_err(|e| {
                    OasisError::Embedding(format!("gemini failed to parse response JSON: {e}"))
                })?;

            let values = parsed["embedding"]["values"]
                .as_array()
                .ok_or_else(|| {
                    OasisError::Embedding(
                        "missing embedding.values in gemini response".to_string(),
                    )
                })?;

            let embedding: Vec<f32> = values
                .iter()
                .map(|v| {
                    v.as_f64()
                        .ok_or_else(|| {
                            OasisError::Embedding(
                                "non-numeric value in gemini embedding array".to_string(),
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
        "gemini"
    }
}
