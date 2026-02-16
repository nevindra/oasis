use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use oasis_integrations::linear::LinearClient;
use serde_json::json;
use std::sync::Arc;

use crate::tool::{Tool, ToolResult};

pub struct LinearTool {
    client: Arc<LinearClient>,
}

impl LinearTool {
    pub fn new(client: Arc<LinearClient>) -> Self {
        Self { client }
    }
}

#[async_trait]
impl Tool for LinearTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![
            ToolDefinition {
                name: "linear_create_issue".to_string(),
                description: "Create a new issue in Linear. Requires team_id (use linear_list_issues first to discover teams).".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "title": { "type": "string", "description": "Issue title" },
                        "description": { "type": "string", "description": "Issue description (markdown)" },
                        "team_id": { "type": "string", "description": "Team ID to create the issue in" },
                        "assignee_id": { "type": "string", "description": "Assignee user ID" },
                        "priority": { "type": "integer", "description": "Priority: 0=none, 1=urgent, 2=high, 3=medium, 4=low" }
                    },
                    "required": ["title", "team_id"]
                }),
            },
            ToolDefinition {
                name: "linear_list_issues".to_string(),
                description: "List issues from Linear. Can filter by team, status, or assignee. Also returns available teams.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "team_id": { "type": "string", "description": "Filter by team ID" },
                        "status": { "type": "string", "description": "Filter by status name (e.g. 'In Progress', 'Done', 'Todo')" },
                        "assignee_id": { "type": "string", "description": "Filter by assignee user ID" },
                        "list_teams": { "type": "boolean", "description": "If true, list available teams instead of issues" }
                    }
                }),
            },
            ToolDefinition {
                name: "linear_update_issue".to_string(),
                description: "Update a Linear issue's state, assignee, or priority.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "issue_id": { "type": "string", "description": "The issue ID to update" },
                        "state_id": { "type": "string", "description": "New state ID" },
                        "assignee_id": { "type": "string", "description": "New assignee user ID" },
                        "priority": { "type": "integer", "description": "New priority: 0=none, 1=urgent, 2=high, 3=medium, 4=low" }
                    },
                    "required": ["issue_id"]
                }),
            },
            ToolDefinition {
                name: "linear_search".to_string(),
                description: "Search Linear issues by text query.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "query": { "type": "string", "description": "Search query text" }
                    },
                    "required": ["query"]
                }),
            },
        ]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        let result = match name {
            "linear_create_issue" => self.handle_create(args).await,
            "linear_list_issues" => self.handle_list(args).await,
            "linear_update_issue" => self.handle_update(args).await,
            "linear_search" => self.handle_search(args).await,
            _ => return ToolResult::err(format!("Unknown linear tool: {name}")),
        };
        match result {
            Ok(r) => ToolResult::ok(r),
            Err(e) => ToolResult::err(e.to_string()),
        }
    }
}

impl LinearTool {
    async fn handle_create(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let title = args["title"].as_str().unwrap_or("");
        let description = args["description"].as_str();
        let team_id = args["team_id"].as_str().unwrap_or("");
        let assignee_id = args["assignee_id"].as_str();
        let priority = args["priority"].as_i64().map(|p| p as i32);

        let issue = self
            .client
            .create_issue(title, description, team_id, assignee_id, priority)
            .await?;

        let mut result = format!(
            "Created issue **{}**: {}\nURL: {}",
            issue.identifier, issue.title, issue.url
        );
        if let Some(state) = &issue.state {
            result.push_str(&format!("\nStatus: {}", state.name));
        }
        if let Some(assignee) = &issue.assignee {
            result.push_str(&format!("\nAssignee: {}", assignee.name));
        }
        Ok(result)
    }

    async fn handle_list(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        // Check if user wants to list teams
        if args["list_teams"].as_bool().unwrap_or(false) {
            let teams = self.client.list_teams().await?;
            if teams.is_empty() {
                return Ok("No teams found.".to_string());
            }
            let mut result = String::from("Linear teams:\n");
            for team in &teams {
                result.push_str(&format!("- **{}** ({}) — id: {}\n", team.name, team.key, team.id));
            }
            return Ok(result.trim_end().to_string());
        }

        let team_id = args["team_id"].as_str();
        let status = args["status"].as_str();
        let assignee_id = args["assignee_id"].as_str();

        let issues = self
            .client
            .list_issues(team_id, status, assignee_id, Some(20))
            .await?;

        if issues.is_empty() {
            return Ok("No issues found.".to_string());
        }

        let mut result = String::new();
        for issue in &issues {
            let status = issue
                .state
                .as_ref()
                .map(|s| s.name.as_str())
                .unwrap_or("?");
            let assignee = issue
                .assignee
                .as_ref()
                .map(|a| a.name.as_str())
                .unwrap_or("unassigned");
            result.push_str(&format!(
                "- [{}] **{}** {} ({})\n",
                status, issue.identifier, issue.title, assignee
            ));
        }
        Ok(result.trim_end().to_string())
    }

    async fn handle_update(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let issue_id = args["issue_id"].as_str().unwrap_or("");
        let state_id = args["state_id"].as_str();
        let assignee_id = args["assignee_id"].as_str();
        let priority = args["priority"].as_i64().map(|p| p as i32);

        let issue = self
            .client
            .update_issue(issue_id, state_id, assignee_id, priority)
            .await?;

        let mut result = format!("Updated **{}**: {}", issue.identifier, issue.title);
        if let Some(state) = &issue.state {
            result.push_str(&format!("\nStatus: {}", state.name));
        }
        if let Some(assignee) = &issue.assignee {
            result.push_str(&format!("\nAssignee: {}", assignee.name));
        }
        Ok(result)
    }

    async fn handle_search(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let query = args["query"].as_str().unwrap_or("");
        let issues = self.client.search_issues(query).await?;

        if issues.is_empty() {
            return Ok(format!("No issues found matching \"{query}\"."));
        }

        let mut result = String::new();
        for issue in &issues {
            let status = issue
                .state
                .as_ref()
                .map(|s| s.name.as_str())
                .unwrap_or("?");
            result.push_str(&format!(
                "- [{}] **{}** {}\n  {}\n",
                status,
                issue.identifier,
                issue.title,
                issue.url
            ));
        }
        Ok(result.trim_end().to_string())
    }
}
