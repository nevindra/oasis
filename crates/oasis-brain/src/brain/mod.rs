use std::sync::Arc;

use crate::agent::AgentManager;
use crate::service::ingest::pipeline::IngestPipeline;
use crate::service::llm::{Embedder, LlmDispatch};
use crate::service::memory::MemoryStore;
use crate::service::search::WebSearch;
use crate::service::store::VectorStore;
use crate::service::tasks::TaskManager;
use crate::tool::knowledge::KnowledgeTool;
use crate::tool::memory_tool::MemoryTool;
use crate::tool::schedule::ScheduleTool;
use crate::tool::search::SearchTool;
use crate::tool::task::TaskTool;
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
    pub(crate) tasks: Arc<TaskManager>,
    pub(crate) memory: MemoryStore,
    pub(crate) bot: TelegramBot,
    pub(crate) config: Config,
    pub(crate) tools: ToolRegistry,
    pub(crate) llm: LlmDispatch,
    pub(crate) embedder: Arc<Embedder>,
    pub(crate) search_tool: Arc<SearchTool>,
    pub(crate) memory_tool: Arc<MemoryTool>,
    pub(crate) agent_manager: Arc<AgentManager>,
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

        // TaskManager (separate DB connection)
        let task_db = if !config.database.turso_url.is_empty() {
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
        let tasks = Arc::new(TaskManager::new(task_db));

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
        let task_tool = TaskTool::new(Arc::clone(&tasks), tz);
        let search_tool = Arc::new(SearchTool::new(Arc::clone(&search), Arc::clone(&embedder)));
        let knowledge_tool =
            KnowledgeTool::new(Arc::clone(&store), Arc::clone(&embedder), config.brain.vector_top_k);
        let schedule_tool = ScheduleTool::new(Arc::clone(&store), tz);
        let memory_tool = Arc::new(MemoryTool::new(
            pipeline,
            Arc::clone(&store),
            Arc::clone(&embedder),
        ));

        let tools = ToolRegistry::new(vec![
            Box::new(task_tool),
            Box::new(Arc::clone(&search_tool)),
            Box::new(knowledge_tool),
            Box::new(schedule_tool),
            Box::new(Arc::clone(&memory_tool)),
        ]);

        let agent_manager = Arc::new(AgentManager::new(3));

        Ok(Self {
            store,
            tasks,
            memory,
            bot,
            config,
            tools,
            llm,
            embedder,
            search_tool,
            memory_tool,
            agent_manager,
        })
    }
}
