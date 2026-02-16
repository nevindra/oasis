use oasis_core::error::Result;
use oasis_core::types::{ChatRequest, ChatResponse, ToolDefinition};
use tokio::sync::mpsc;

/// Trait for LLM chat completion providers.
pub trait LlmProvider: Send + Sync {
    /// Send a chat request and receive a completion response.
    fn chat(&self, request: ChatRequest) -> impl std::future::Future<Output = Result<ChatResponse>> + Send;

    /// Send a chat request with tool definitions. The LLM may return tool calls
    /// in the response's `tool_calls` field instead of (or alongside) text content.
    /// Default implementation falls back to `chat` (ignoring tools).
    fn chat_with_tools(
        &self,
        request: ChatRequest,
        _tools: &[ToolDefinition],
    ) -> impl std::future::Future<Output = Result<ChatResponse>> + Send {
        self.chat(request)
    }

    /// Stream a chat response, sending text chunks through the channel.
    /// Returns the final complete response with usage info.
    /// Default implementation falls back to non-streaming `chat`.
    fn chat_stream(
        &self,
        request: ChatRequest,
        tx: mpsc::UnboundedSender<String>,
    ) -> impl std::future::Future<Output = Result<ChatResponse>> + Send {
        async move {
            let response = self.chat(request).await?;
            let _ = tx.send(response.content.clone());
            Ok(response)
        }
    }

    /// Return the provider name (e.g. "anthropic", "openai").
    fn name(&self) -> &str;
}

/// Trait for text embedding providers.
pub trait EmbeddingProvider: Send + Sync {
    /// Embed one or more text strings, returning a vector of embeddings.
    fn embed(&self, texts: &[&str]) -> impl std::future::Future<Output = Result<Vec<Vec<f32>>>> + Send;

    /// Return the dimensionality of the embedding vectors.
    fn dimensions(&self) -> usize;

    /// Return the provider name (e.g. "openai", "ollama").
    fn name(&self) -> &str;
}
