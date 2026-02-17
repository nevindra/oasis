use std::sync::Arc;

use crate::agent::AgentManager;
use crate::service::ingest::pipeline::IngestPipeline;
use crate::service::llm::{Embedder, LlmDispatch};
use crate::service::memory::MemoryStore;
use crate::service::search::WebSearch;
use crate::service::skills::SkillManager;
use crate::service::store::VectorStore;
use crate::tool::knowledge::KnowledgeTool;
use crate::tool::memory_tool::MemoryTool;
use crate::tool::schedule::ScheduleTool;
use crate::tool::search::SearchTool;
use crate::tool::ToolRegistry;
use oasis_core::config::Config;
use oasis_core::error::{OasisError, Result};
use oasis_telegram::bot::TelegramBot;

mod action;
mod chat;
mod router;
mod scheduling;
mod storage;

/// The main orchestration struct that ties all components together.
///
/// Brain is the engine: it routes messages, manages streaming, and delegates
/// tool execution to the ToolRegistry. Domain logic lives in tool implementations.
pub struct Brain {
    pub(crate) store: Arc<VectorStore>,
    pub(crate) memory: MemoryStore,
    pub(crate) bot: TelegramBot,
    pub(crate) config: Config,
    pub(crate) tools: ToolRegistry,
    pub(crate) llm: LlmDispatch,
    pub(crate) embedder: Arc<Embedder>,
    pub(crate) search_tool: Arc<SearchTool>,
    pub(crate) memory_tool: Arc<MemoryTool>,
    pub(crate) agent_manager: Arc<AgentManager>,
    pub(crate) skill_manager: SkillManager,
}

/// Implement TokenStore for VectorStore so integrations can store OAuth tokens.
#[async_trait::async_trait]
impl oasis_integrations::TokenStore for VectorStore {
    async fn get(&self, key: &str) -> Result<Option<String>> {
        self.get_config(key).await
    }
    async fn set(&self, key: &str, value: &str) -> Result<()> {
        self.set_config(key, value).await
    }
}

impl Brain {
    /// Initialize a new Brain with the given configuration.
    pub async fn new(config: Config) -> Result<Self> {
        // VectorStore
        let store = if !config.database.turso_url.is_empty() {
            VectorStore::new_remote(&config.database.turso_url, &config.database.turso_token)
                .await?
        } else {
            VectorStore::new(&config.database.path).await?
        };
        let store = Arc::new(store);

        // MemoryStore (separate DB connection)
        let memory_db = if !config.database.turso_url.is_empty() {
            libsql::Builder::new_remote(
                config.database.turso_url.clone(),
                config.database.turso_token.clone(),
            )
            .build()
            .await
            .map_err(|e| OasisError::Database(e.to_string()))?
        } else {
            libsql::Builder::new_local(&config.database.path)
                .build()
                .await
                .map_err(|e| OasisError::Database(e.to_string()))?
        };
        let memory = MemoryStore::new(memory_db);
        memory.init().await?;

        let pipeline =
            IngestPipeline::new(config.chunking.max_tokens, config.chunking.overlap_tokens);
        let bot = TelegramBot::new(
            config.telegram.token.clone(),
            config.telegram.allowed_user_id,
        );
        let search = Arc::new(WebSearch::new().await?);

        // Extracted engine components
        let llm = LlmDispatch::new(config.clone());
        let embedder = Arc::new(Embedder::new(config.clone()));

        // Tool implementations
        let tz = config.brain.timezone_offset;
        let search_tool = Arc::new(SearchTool::new(Arc::clone(&search), Arc::clone(&embedder)));
        let knowledge_tool =
            KnowledgeTool::new(Arc::clone(&store), Arc::clone(&embedder), config.brain.vector_top_k);
        let schedule_tool = ScheduleTool::new(Arc::clone(&store), tz);
        let memory_tool = Arc::new(MemoryTool::new(
            pipeline,
            Arc::clone(&store),
            Arc::clone(&embedder),
        ));

        // Skill management tool
        let skill_tool = crate::tool::skill::SkillTool::new(Arc::clone(&store), Arc::clone(&embedder));

        // Shell tool (sandboxed to workspace)
        let workspace = config.brain.workspace_path.clone();
        let shell_tool = crate::tool::shell::ShellTool::new(workspace.clone(), 60);

        // File tool (sandboxed to workspace)
        let file_tool = crate::tool::file::FileTool::new(workspace);

        // HTTP tool
        let http_tool = crate::tool::http::HttpTool::new();

        let mut tool_list: Vec<Box<dyn crate::tool::Tool>> = vec![
            Box::new(Arc::clone(&search_tool)),
            Box::new(knowledge_tool),
            Box::new(schedule_tool),
            Box::new(Arc::clone(&memory_tool)),
            Box::new(skill_tool),
            Box::new(shell_tool),
            Box::new(file_tool),
            Box::new(http_tool),
        ];

        // --- Conditional integration tools ---

        // Linear (API key auth)
        if !config.integrations.linear.api_key.is_empty() {
            log!(" [integrations] Linear enabled");
            let linear_client = Arc::new(
                oasis_integrations::linear::LinearClient::new(
                    config.integrations.linear.api_key.clone(),
                ),
            );
            tool_list.push(Box::new(crate::tool::linear::LinearTool::new(linear_client)));
        }

        // Google Calendar + Gmail (OAuth)
        if !config.integrations.google.client_id.is_empty() {
            log!(" [integrations] Google enabled (Calendar + Gmail)");
            let google_auth = Arc::new(oasis_integrations::google::GoogleAuth::new(
                config.integrations.google.client_id.clone(),
                config.integrations.google.client_secret.clone(),
                config.integrations.google.callback_url.clone(),
                Arc::clone(&store) as Arc<dyn oasis_integrations::TokenStore>,
            ));

            let calendar_client = Arc::new(
                oasis_integrations::google::calendar::CalendarClient::new(Arc::clone(&google_auth)),
            );
            let gmail_client = Arc::new(
                oasis_integrations::google::gmail::GmailClient::new(Arc::clone(&google_auth)),
            );

            tool_list.push(Box::new(crate::tool::calendar::CalendarTool::new(
                calendar_client,
                Arc::clone(&google_auth),
            )));
            tool_list.push(Box::new(crate::tool::gmail::GmailTool::new(gmail_client)));

            // Start OAuth callback server in background
            let port = config.integrations.server.port;
            let auth_for_server = Arc::clone(&google_auth);
            tokio::spawn(async move {
                log!(" [integrations] OAuth server starting on port {port}");
                if let Err(e) = oasis_integrations::http::start_oauth_server(port, auth_for_server).await {
                    log!(" [integrations] OAuth server error: {e}");
                }
            });
        }

        let tools = ToolRegistry::new(tool_list);

        let skill_manager = SkillManager::new(Arc::clone(&store), Arc::clone(&embedder), llm.clone());

        let agent_manager = Arc::new(AgentManager::new(3));

        Ok(Self {
            store,
            memory,
            bot,
            config,
            tools,
            llm,
            embedder,
            search_tool,
            memory_tool,
            agent_manager,
            skill_manager,
        })
    }
}
