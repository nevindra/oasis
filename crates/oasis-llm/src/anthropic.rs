use oasis_core::error::{OasisError, Result};
use oasis_core::types::{ChatRequest, ChatResponse, ToolCallRequest, ToolDefinition, Usage};
use reqwest::Client;
use serde_json::json;

use crate::provider::LlmProvider;

const ANTHROPIC_API_URL: &str = "https://api.anthropic.com/v1/messages";
const ANTHROPIC_VERSION: &str = "2023-06-01";

/// Anthropic Claude LLM provider.
pub struct AnthropicLlm {
    client: Client,
    api_key: String,
    model: String,
}

impl AnthropicLlm {
    /// Create a new Anthropic LLM provider.
    ///
    /// # Arguments
    /// * `api_key` - Anthropic API key
    /// * `model` - Model identifier (e.g. "claude-sonnet-4-20250514")
    pub fn new(api_key: String, model: String) -> Self {
        Self {
            client: Client::new(),
            api_key,
            model,
        }
    }

    /// Build messages array for the Anthropic API.
    /// Handles text messages, tool call messages, and tool result messages.
    fn build_messages(request: &ChatRequest) -> (Option<String>, Vec<serde_json::Value>) {
        let mut system = None;
        let mut messages = Vec::new();

        for m in &request.messages {
            if m.role == "system" {
                system = Some(m.content.clone());
            } else if !m.tool_calls.is_empty() {
                // Assistant message with tool calls
                let mut content: Vec<serde_json::Value> = Vec::new();
                if !m.content.is_empty() {
                    content.push(json!({ "type": "text", "text": m.content }));
                }
                for tc in &m.tool_calls {
                    content.push(json!({
                        "type": "tool_use",
                        "id": tc.id,
                        "name": tc.name,
                        "input": tc.arguments,
                    }));
                }
                messages.push(json!({ "role": "assistant", "content": content }));
            } else if m.role == "tool" {
                // Tool result message
                let tool_use_id = m.tool_call_id.as_deref().unwrap_or("");
                messages.push(json!({
                    "role": "user",
                    "content": [{
                        "type": "tool_result",
                        "tool_use_id": tool_use_id,
                        "content": m.content,
                    }]
                }));
            } else {
                messages.push(json!({
                    "role": m.role,
                    "content": m.content,
                }));
            }
        }

        (system, messages)
    }
}

impl LlmProvider for AnthropicLlm {
    async fn chat(&self, request: ChatRequest) -> Result<ChatResponse> {
        let (system, messages) = Self::build_messages(&request);

        let max_tokens = request.max_tokens.unwrap_or(4096);

        let mut body = json!({
            "model": self.model,
            "max_tokens": max_tokens,
            "messages": messages,
        });

        if let Some(sys) = system {
            body.as_object_mut()
                .unwrap()
                .insert("system".to_string(), json!(sys));
        }

        if let Some(temp) = request.temperature {
            body.as_object_mut()
                .unwrap()
                .insert("temperature".to_string(), json!(temp));
        }

        let response = self
            .client
            .post(ANTHROPIC_API_URL)
            .header("x-api-key", &self.api_key)
            .header("anthropic-version", ANTHROPIC_VERSION)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Llm {
                provider: "anthropic".to_string(),
                message: format!("request failed: {e}"),
            })?;

        let status = response.status().as_u16();
        let response_text = response.text().await.map_err(|e| OasisError::Llm {
            provider: "anthropic".to_string(),
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
                provider: "anthropic".to_string(),
                message: format!("failed to parse response JSON: {e}"),
            })?;

        let content = parsed["content"]
            .as_array()
            .and_then(|arr| arr.first())
            .and_then(|block| block["text"].as_str())
            .unwrap_or("")
            .to_string();

        let usage = match (
            parsed["usage"]["input_tokens"].as_u64(),
            parsed["usage"]["output_tokens"].as_u64(),
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
        let (system, messages) = Self::build_messages(&request);

        let max_tokens = request.max_tokens.unwrap_or(4096);

        let anthropic_tools: Vec<serde_json::Value> = tools.iter().map(|t| {
            json!({
                "name": t.name,
                "description": t.description,
                "input_schema": t.parameters,
            })
        }).collect();

        let mut body = json!({
            "model": self.model,
            "max_tokens": max_tokens,
            "messages": messages,
            "tools": anthropic_tools,
        });

        if let Some(sys) = system {
            body.as_object_mut()
                .unwrap()
                .insert("system".to_string(), json!(sys));
        }

        if let Some(temp) = request.temperature {
            body.as_object_mut()
                .unwrap()
                .insert("temperature".to_string(), json!(temp));
        }

        let response = self
            .client
            .post(ANTHROPIC_API_URL)
            .header("x-api-key", &self.api_key)
            .header("anthropic-version", ANTHROPIC_VERSION)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Llm {
                provider: "anthropic".to_string(),
                message: format!("request failed: {e}"),
            })?;

        let status = response.status().as_u16();
        let response_text = response.text().await.map_err(|e| OasisError::Llm {
            provider: "anthropic".to_string(),
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
                provider: "anthropic".to_string(),
                message: format!("failed to parse response JSON: {e}"),
            })?;

        let mut content = String::new();
        let mut tool_calls = Vec::new();

        if let Some(blocks) = parsed["content"].as_array() {
            for block in blocks {
                match block["type"].as_str() {
                    Some("text") => {
                        if let Some(text) = block["text"].as_str() {
                            content.push_str(text);
                        }
                    }
                    Some("tool_use") => {
                        let id = block["id"].as_str().unwrap_or("").to_string();
                        let name = block["name"].as_str().unwrap_or("").to_string();
                        let arguments = block["input"].clone();
                        tool_calls.push(ToolCallRequest { id, name, arguments, metadata: None });
                    }
                    _ => {}
                }
            }
        }

        let usage = match (
            parsed["usage"]["input_tokens"].as_u64(),
            parsed["usage"]["output_tokens"].as_u64(),
        ) {
            (Some(input), Some(output)) => Some(Usage {
                input_tokens: input as u32,
                output_tokens: output as u32,
            }),
            _ => None,
        };

        Ok(ChatResponse { content, tool_calls, usage })
    }

    fn name(&self) -> &str {
        "anthropic"
    }
}
