use oasis_core::config::Config;
use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;
use oasis_llm::anthropic::AnthropicLlm;
use oasis_llm::gemini::{GeminiEmbedding, GeminiLlm};
use oasis_llm::ollama::{OllamaEmbedding, OllamaLlm};
use oasis_llm::openai::{OpenAiEmbedding, OpenAiLlm};
use oasis_llm::provider::{EmbeddingProvider, LlmProvider};

/// Encapsulates embedding provider dispatch.
///
/// Creates providers on-the-fly from config, matching the existing pattern
/// of avoiding async trait objects.
pub struct Embedder {
    config: Config,
}

impl Embedder {
    pub fn new(config: Config) -> Self {
        Self { config }
    }

    pub async fn embed(&self, texts: &[&str]) -> Result<Vec<Vec<f32>>> {
        let provider = &self.config.embedding.provider;
        let model = &self.config.embedding.model;
        log!(" [embed] calling {provider}/{model} for {} text(s)", texts.len());
        let result = self.embed_inner(texts).await;
        match &result {
            Ok(vecs) => {
                let dims = vecs.first().map(|v| v.len()).unwrap_or(0);
                log!(" [embed] OK, {} vector(s) x {dims} dims", vecs.len());
            }
            Err(e) => log!(" [embed] ERROR: {e}"),
        }
        result
    }

    async fn embed_inner(&self, texts: &[&str]) -> Result<Vec<Vec<f32>>> {
        match self.config.embedding.provider.as_str() {
            "openai" => {
                let provider = OpenAiEmbedding::new(
                    self.config.embedding.api_key.clone(),
                    self.config.embedding.model.clone(),
                    self.config.embedding.dimensions,
                );
                provider.embed(texts).await
            }
            "gemini" => {
                let provider = GeminiEmbedding::new(
                    self.config.embedding.api_key.clone(),
                    self.config.embedding.model.clone(),
                    self.config.embedding.dimensions,
                );
                provider.embed(texts).await
            }
            "ollama" => {
                let provider = OllamaEmbedding::new(
                    self.config.ollama.base_url.clone(),
                    self.config.embedding.model.clone(),
                    self.config.embedding.dimensions,
                );
                provider.embed(texts).await
            }
            other => Err(OasisError::Config(format!(
                "unknown embedding provider: '{other}'. Supported: openai, gemini, ollama"
            ))),
        }
    }
}

#[derive(Clone)]
pub struct LlmDispatch {
    config: Config,
}

impl LlmDispatch {
    pub fn new(config: Config) -> Self {
        Self { config }
    }

    pub async fn chat_stream(
        &self,
        request: ChatRequest,
        tx: tokio::sync::mpsc::UnboundedSender<String>,
    ) -> Result<ChatResponse> {
        let provider = &self.config.llm.provider;
        let model = &self.config.llm.model;
        log!(" [chat-llm] calling {provider}/{model} (stream)");
        match provider.as_str() {
            "gemini" => {
                let provider = GeminiLlm::new(
                    self.config.llm.api_key.clone(),
                    self.config.llm.model.clone(),
                );
                provider.chat_stream(request, tx).await
            }
            "anthropic" => {
                let provider = AnthropicLlm::new(
                    self.config.llm.api_key.clone(),
                    self.config.llm.model.clone(),
                );
                provider.chat_stream(request, tx).await
            }
            "openai" => {
                let provider = OpenAiLlm::new(
                    self.config.llm.api_key.clone(),
                    self.config.llm.model.clone(),
                );
                provider.chat_stream(request, tx).await
            }
            "ollama" => {
                let provider = OllamaLlm::new(
                    self.config.ollama.base_url.clone(),
                    self.config.llm.model.clone(),
                );
                provider.chat_stream(request, tx).await
            }
            other => Err(OasisError::Config(format!(
                "unknown LLM provider: '{other}'"
            ))),
        }
    }

    pub async fn chat_with_tools(
        &self,
        request: ChatRequest,
        tools: &[ToolDefinition],
    ) -> Result<ChatResponse> {
        let action = &self.config.action;
        match action.provider.as_str() {
            "gemini" => {
                let provider = GeminiLlm::new(
                    action.api_key.clone(),
                    action.model.clone(),
                );
                provider.chat_with_tools(request, tools).await
            }
            "anthropic" => {
                let provider = AnthropicLlm::new(
                    action.api_key.clone(),
                    action.model.clone(),
                );
                provider.chat_with_tools(request, tools).await
            }
            "openai" => {
                let provider = OpenAiLlm::new(
                    action.api_key.clone(),
                    action.model.clone(),
                );
                provider.chat_with_tools(request, tools).await
            }
            "ollama" => {
                let provider = OllamaLlm::new(
                    self.config.ollama.base_url.clone(),
                    action.model.clone(),
                );
                provider.chat_with_tools(request, tools).await
            }
            other => Err(OasisError::Config(format!(
                "unknown action LLM provider: '{other}'"
            ))),
        }
    }

    pub async fn chat_intent(&self, request: ChatRequest) -> Result<ChatResponse> {
        let provider = &self.config.intent.provider;
        let model = &self.config.intent.model;

        let max_retries = 3;
        let mut last_err = None;

        for attempt in 0..=max_retries {
            if attempt > 0 {
                let delay = std::time::Duration::from_secs(1 << (attempt - 1));
                log!(" [intent-llm] retry {attempt}/{max_retries} in {}s", delay.as_secs());
                tokio::time::sleep(delay).await;
            }

            log!(" [intent-llm] calling {provider}/{model}");

            let result = match provider.as_str() {
                "gemini" => {
                    let p = GeminiLlm::new(
                        self.config.intent.api_key.clone(),
                        model.clone(),
                    );
                    p.chat(request.clone()).await
                }
                "anthropic" => {
                    let p = AnthropicLlm::new(
                        self.config.intent.api_key.clone(),
                        model.clone(),
                    );
                    p.chat(request.clone()).await
                }
                "openai" => {
                    let p = OpenAiLlm::new(
                        self.config.intent.api_key.clone(),
                        model.clone(),
                    );
                    p.chat(request.clone()).await
                }
                "ollama" => {
                    let p = OllamaLlm::new(
                        self.config.ollama.base_url.clone(),
                        model.clone(),
                    );
                    p.chat(request.clone()).await
                }
                other => {
                    return Err(OasisError::Config(format!(
                        "unknown intent provider: '{other}'"
                    )));
                }
            };

            match result {
                Ok(response) => return Ok(response),
                Err(e) => {
                    if is_transient_error(&e) && attempt < max_retries {
                        log!(" [intent-llm] transient error: {e}");
                        last_err = Some(e);
                        continue;
                    }
                    return Err(e);
                }
            }
        }

        Err(last_err.unwrap_or_else(|| OasisError::Llm {
            provider: provider.clone(),
            message: "max retries exceeded".to_string(),
        }))
    }
}

pub fn is_transient_error(err: &OasisError) -> bool {
    match err {
        OasisError::Http { status, .. } => matches!(status, 429 | 500 | 502 | 503 | 504),
        OasisError::Llm { message, .. } => {
            message.contains("timed out")
                || message.contains("connection")
                || message.contains("temporarily")
        }
        _ => false,
    }
}
