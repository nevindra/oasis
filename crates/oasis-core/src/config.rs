use serde::{Deserialize, Serialize};
use std::path::Path;

use crate::error::{OasisError, Result};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Config {
    #[serde(default)]
    pub telegram: TelegramConfig,
    #[serde(default)]
    pub llm: LlmConfig,
    #[serde(default)]
    pub embedding: EmbeddingConfig,
    #[serde(default)]
    pub ollama: OllamaConfig,
    #[serde(default)]
    pub database: DatabaseConfig,
    #[serde(default)]
    pub chunking: ChunkingConfig,
    #[serde(default)]
    pub brain: BrainConfig,
    #[serde(default)]
    pub intent: IntentConfig,
    /// Optional separate model for agentic tool use (action loop).
    /// Falls back to `llm` if not configured.
    #[serde(default)]
    pub action: ActionConfig,
    #[serde(default)]
    pub integrations: IntegrationsConfig,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TelegramConfig {
    #[serde(default)]
    pub allowed_user_id: i64,
    #[serde(default)]
    pub token: String,
}

impl Default for TelegramConfig {
    fn default() -> Self {
        Self {
            allowed_user_id: 0,
            token: String::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LlmConfig {
    #[serde(default = "default_llm_provider")]
    pub provider: String,
    #[serde(default = "default_llm_model")]
    pub model: String,
    #[serde(default)]
    pub api_key: String,
}

fn default_llm_provider() -> String {
    "anthropic".to_string()
}

fn default_llm_model() -> String {
    "claude-sonnet-4-5-20250929".to_string()
}

impl Default for LlmConfig {
    fn default() -> Self {
        Self {
            provider: default_llm_provider(),
            model: default_llm_model(),
            api_key: String::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EmbeddingConfig {
    #[serde(default = "default_embedding_provider")]
    pub provider: String,
    #[serde(default = "default_embedding_model")]
    pub model: String,
    #[serde(default = "default_dimensions")]
    pub dimensions: usize,
    #[serde(default)]
    pub api_key: String,
}

fn default_embedding_provider() -> String {
    "openai".to_string()
}

fn default_embedding_model() -> String {
    "text-embedding-3-small".to_string()
}

fn default_dimensions() -> usize {
    1536
}

impl Default for EmbeddingConfig {
    fn default() -> Self {
        Self {
            provider: default_embedding_provider(),
            model: default_embedding_model(),
            dimensions: default_dimensions(),
            api_key: String::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OllamaConfig {
    #[serde(default = "default_ollama_url")]
    pub base_url: String,
}

fn default_ollama_url() -> String {
    "http://localhost:11434".to_string()
}

impl Default for OllamaConfig {
    fn default() -> Self {
        Self {
            base_url: default_ollama_url(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DatabaseConfig {
    #[serde(default = "default_db_path")]
    pub path: String,
    #[serde(default)]
    pub turso_url: String,
    #[serde(default)]
    pub turso_token: String,
}

fn default_db_path() -> String {
    "oasis.db".to_string()
}

impl Default for DatabaseConfig {
    fn default() -> Self {
        Self {
            path: default_db_path(),
            turso_url: String::new(),
            turso_token: String::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChunkingConfig {
    #[serde(default = "default_max_tokens")]
    pub max_tokens: usize,
    #[serde(default = "default_overlap")]
    pub overlap_tokens: usize,
}

fn default_max_tokens() -> usize {
    512
}

fn default_overlap() -> usize {
    50
}

impl Default for ChunkingConfig {
    fn default() -> Self {
        Self {
            max_tokens: default_max_tokens(),
            overlap_tokens: default_overlap(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BrainConfig {
    #[serde(default = "default_context_window")]
    pub context_window: usize,
    #[serde(default = "default_top_k")]
    pub vector_top_k: usize,
    /// UTC offset in hours (e.g., 7 for WIB/UTC+7, -5 for EST/UTC-5).
    #[serde(default = "default_timezone_offset")]
    pub timezone_offset: i32,
}

fn default_context_window() -> usize {
    20
}

fn default_top_k() -> usize {
    10
}

fn default_timezone_offset() -> i32 {
    7 // WIB (UTC+7)
}

impl Default for BrainConfig {
    fn default() -> Self {
        Self {
            context_window: default_context_window(),
            vector_top_k: default_top_k(),
            timezone_offset: default_timezone_offset(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IntentConfig {
    #[serde(default = "default_intent_provider")]
    pub provider: String,
    #[serde(default = "default_intent_model")]
    pub model: String,
    #[serde(default)]
    pub api_key: String,
}

fn default_intent_provider() -> String {
    "gemini".to_string()
}

fn default_intent_model() -> String {
    "gemini-2.5-flash-lite".to_string()
}

impl Default for IntentConfig {
    fn default() -> Self {
        Self {
            provider: default_intent_provider(),
            model: default_intent_model(),
            api_key: String::new(),
        }
    }
}

/// Separate model config for agentic tool-use (action loop).
/// Empty provider/model means "fall back to [llm]".
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ActionConfig {
    #[serde(default)]
    pub provider: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub api_key: String,
}

impl Default for ActionConfig {
    fn default() -> Self {
        Self {
            provider: String::new(),
            model: String::new(),
            api_key: String::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IntegrationsConfig {
    #[serde(default)]
    pub linear: LinearConfig,
    #[serde(default)]
    pub google: GoogleConfig,
    #[serde(default)]
    pub server: IntegrationServerConfig,
}

impl Default for IntegrationsConfig {
    fn default() -> Self {
        Self {
            linear: LinearConfig::default(),
            google: GoogleConfig::default(),
            server: IntegrationServerConfig::default(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LinearConfig {
    #[serde(default)]
    pub api_key: String,
}

impl Default for LinearConfig {
    fn default() -> Self {
        Self {
            api_key: String::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GoogleConfig {
    #[serde(default)]
    pub client_id: String,
    #[serde(default)]
    pub client_secret: String,
    #[serde(default = "default_callback_url")]
    pub callback_url: String,
}

fn default_callback_url() -> String {
    "http://localhost:8080/oauth/callback".to_string()
}

impl Default for GoogleConfig {
    fn default() -> Self {
        Self {
            client_id: String::new(),
            client_secret: String::new(),
            callback_url: default_callback_url(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IntegrationServerConfig {
    #[serde(default = "default_server_port")]
    pub port: u16,
}

fn default_server_port() -> u16 {
    8080
}

impl Default for IntegrationServerConfig {
    fn default() -> Self {
        Self {
            port: default_server_port(),
        }
    }
}

impl Config {
    /// Load config: defaults → oasis.toml → env vars (env wins).
    pub fn load(path: &Path) -> Result<Self> {
        let mut config = if path.exists() {
            let content = std::fs::read_to_string(path)
                .map_err(|e| OasisError::Config(format!("failed to read config: {e}")))?;
            toml::from_str(&content)
                .map_err(|e| OasisError::Config(format!("failed to parse config: {e}")))?
        } else {
            Self::default()
        };

        // Override with env vars
        if let Ok(v) = std::env::var("OASIS_TELEGRAM_TOKEN") {
            config.telegram.token = v;
        }
        if let Ok(v) = std::env::var("OASIS_LLM_API_KEY") {
            config.llm.api_key = v;
        }
        if let Ok(v) = std::env::var("OASIS_EMBEDDING_API_KEY") {
            config.embedding.api_key = v;
        }
        if let Ok(v) = std::env::var("OASIS_TURSO_URL") {
            config.database.turso_url = v;
        }
        if let Ok(v) = std::env::var("OASIS_TURSO_TOKEN") {
            config.database.turso_token = v;
        }
        if let Ok(v) = std::env::var("OASIS_INTENT_API_KEY") {
            config.intent.api_key = v;
        }

        if let Ok(v) = std::env::var("OASIS_ACTION_API_KEY") {
            config.action.api_key = v;
        }
        if let Ok(v) = std::env::var("OASIS_LINEAR_API_KEY") {
            config.integrations.linear.api_key = v;
        }
        if let Ok(v) = std::env::var("OASIS_GOOGLE_CLIENT_ID") {
            config.integrations.google.client_id = v;
        }
        if let Ok(v) = std::env::var("OASIS_GOOGLE_CLIENT_SECRET") {
            config.integrations.google.client_secret = v;
        }

        // Fallback: intent model uses the LLM API key if not separately configured
        if config.intent.api_key.is_empty() {
            config.intent.api_key = config.llm.api_key.clone();
        }

        // Fallback: action model uses the LLM config if not separately configured
        if config.action.provider.is_empty() {
            config.action.provider = config.llm.provider.clone();
            config.action.model = config.llm.model.clone();
        }
        if config.action.api_key.is_empty() {
            config.action.api_key = config.llm.api_key.clone();
        }

        Ok(config)
    }
}

impl Default for Config {
    fn default() -> Self {
        Self {
            telegram: TelegramConfig::default(),
            llm: LlmConfig::default(),
            embedding: EmbeddingConfig::default(),
            ollama: OllamaConfig::default(),
            database: DatabaseConfig::default(),
            chunking: ChunkingConfig::default(),
            brain: BrainConfig::default(),
            intent: IntentConfig::default(),
            action: ActionConfig::default(),
            integrations: IntegrationsConfig::default(),
        }
    }
}
