pub mod task;
pub mod search;
pub mod knowledge;
pub mod schedule;
pub mod memory_tool;
pub mod linear;
pub mod calendar;
pub mod gmail;
pub mod skill;
pub mod shell;
pub mod file;
pub mod http;

use std::sync::Arc;

use async_trait::async_trait;
use oasis_core::types::ToolDefinition;

/// Result of executing a tool.
pub struct ToolResult {
    pub output: String,
}

impl ToolResult {
    pub fn ok(output: impl Into<String>) -> Self {
        Self { output: output.into() }
    }

    pub fn err(message: impl Into<String>) -> Self {
        Self { output: format!("Error: {}", message.into()) }
    }
}

/// A tool that the LLM can call during the action loop.
///
/// Each tool struct owns its dependencies and can provide multiple
/// tool definitions (e.g. TaskTool provides task_create, task_list, etc.).
#[async_trait]
pub trait Tool: Send + Sync {
    /// Tool definitions this struct provides.
    fn definitions(&self) -> Vec<ToolDefinition>;
    /// Execute a tool call by name. Only called for names in definitions().
    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult;
}

/// Blanket impl so `Arc<T>` can be boxed into the registry when Brain
/// needs to keep a handle to the same tool (e.g. SearchTool, MemoryTool).
#[async_trait]
impl<T: Tool> Tool for Arc<T> {
    fn definitions(&self) -> Vec<ToolDefinition> {
        (**self).definitions()
    }
    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        (**self).execute(name, args).await
    }
}

/// Registry of all available tools. Replaces the match statement in Brain.
pub struct ToolRegistry {
    tools: Vec<Box<dyn Tool>>,
}

impl ToolRegistry {
    pub fn new(tools: Vec<Box<dyn Tool>>) -> Self {
        Self { tools }
    }

    pub fn definitions(&self) -> Vec<ToolDefinition> {
        self.tools.iter().flat_map(|t| t.definitions()).collect()
    }

    pub async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        for tool in &self.tools {
            if tool.definitions().iter().any(|d| d.name == name) {
                return tool.execute(name, args).await;
            }
        }
        ToolResult::err(format!("Unknown tool: {name}"))
    }
}
