use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use oasis_integrations::google::gmail::GmailClient;
use serde_json::json;
use std::sync::Arc;

use crate::tool::{Tool, ToolResult};

pub struct GmailTool {
    client: Arc<GmailClient>,
}

impl GmailTool {
    pub fn new(client: Arc<GmailClient>) -> Self {
        Self { client }
    }
}

#[async_trait]
impl Tool for GmailTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![
            ToolDefinition {
                name: "gmail_search".to_string(),
                description: "Search emails in Gmail. Uses Gmail's search query syntax (e.g. 'from:alice subject:report is:unread').".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "query": { "type": "string", "description": "Gmail search query" },
                        "max_results": { "type": "integer", "description": "Max number of results (default 10)" }
                    },
                    "required": ["query"]
                }),
            },
            ToolDefinition {
                name: "gmail_read".to_string(),
                description: "Read the full content of a specific email by its message ID.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "message_id": { "type": "string", "description": "The Gmail message ID" }
                    },
                    "required": ["message_id"]
                }),
            },
            ToolDefinition {
                name: "gmail_draft".to_string(),
                description: "Create a draft email in Gmail.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "to": { "type": "string", "description": "Recipient email address" },
                        "subject": { "type": "string", "description": "Email subject" },
                        "body": { "type": "string", "description": "Email body text" }
                    },
                    "required": ["to", "subject", "body"]
                }),
            },
            ToolDefinition {
                name: "gmail_send".to_string(),
                description: "Send an email via Gmail. IMPORTANT: Always confirm with the user via ask_user before calling this tool.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "to": { "type": "string", "description": "Recipient email address" },
                        "subject": { "type": "string", "description": "Email subject" },
                        "body": { "type": "string", "description": "Email body text" }
                    },
                    "required": ["to", "subject", "body"]
                }),
            },
            ToolDefinition {
                name: "gmail_reply".to_string(),
                description: "Reply to an email thread. Gets the thread_id and message_id from gmail_read.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "thread_id": { "type": "string", "description": "The thread ID to reply to" },
                        "message_id": { "type": "string", "description": "The message ID being replied to" },
                        "to": { "type": "string", "description": "Recipient email address" },
                        "subject": { "type": "string", "description": "Email subject (will be prefixed with Re: if needed)" },
                        "body": { "type": "string", "description": "Reply body text" }
                    },
                    "required": ["thread_id", "message_id", "to", "subject", "body"]
                }),
            },
        ]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        let result = match name {
            "gmail_search" => self.handle_search(args).await,
            "gmail_read" => self.handle_read(args).await,
            "gmail_draft" => self.handle_draft(args).await,
            "gmail_send" => self.handle_send(args).await,
            "gmail_reply" => self.handle_reply(args).await,
            _ => return ToolResult::err(format!("Unknown gmail tool: {name}")),
        };
        match result {
            Ok(r) => ToolResult::ok(r),
            Err(e) => ToolResult::err(e.to_string()),
        }
    }
}

impl GmailTool {
    async fn handle_search(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let query = args["query"].as_str().unwrap_or("");
        let max = args["max_results"].as_u64().unwrap_or(10) as u32;

        let messages = self.client.search(query, max).await?;

        if messages.is_empty() {
            return Ok(format!("No emails found matching \"{query}\"."));
        }

        let mut result = String::new();
        for msg in &messages {
            result.push_str(&format!(
                "- **{}**\n  From: {} | Date: {}\n  {}\n  ID: {}\n",
                msg.subject, msg.from, msg.date, msg.snippet, msg.id
            ));
        }
        Ok(result.trim_end().to_string())
    }

    async fn handle_read(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let message_id = args["message_id"].as_str().unwrap_or("");
        let msg = self.client.read(message_id).await?;

        // Truncate body if too long
        let body = if msg.body.len() > 3000 {
            format!("{}...\n(truncated, {} chars total)", &msg.body[..3000], msg.body.len())
        } else {
            msg.body.clone()
        };

        Ok(format!(
            "**Subject:** {}\n**From:** {}\n**To:** {}\n**Date:** {}\n**Thread ID:** {}\n**Message ID:** {}\n\n{}",
            msg.subject, msg.from, msg.to, msg.date, msg.thread_id, msg.id, body
        ))
    }

    async fn handle_draft(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let to = args["to"].as_str().unwrap_or("");
        let subject = args["subject"].as_str().unwrap_or("");
        let body = args["body"].as_str().unwrap_or("");

        let draft = self.client.create_draft(to, subject, body).await?;
        Ok(format!(
            "Draft created (ID: {})\nTo: {to}\nSubject: {subject}",
            draft.id
        ))
    }

    async fn handle_send(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let to = args["to"].as_str().unwrap_or("");
        let subject = args["subject"].as_str().unwrap_or("");
        let body = args["body"].as_str().unwrap_or("");

        let msg_id = self.client.send(to, subject, body).await?;
        Ok(format!(
            "Email sent (ID: {msg_id})\nTo: {to}\nSubject: {subject}"
        ))
    }

    async fn handle_reply(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let thread_id = args["thread_id"].as_str().unwrap_or("");
        let message_id = args["message_id"].as_str().unwrap_or("");
        let to = args["to"].as_str().unwrap_or("");
        let subject = args["subject"].as_str().unwrap_or("");
        let body = args["body"].as_str().unwrap_or("");

        let msg_id = self
            .client
            .reply(thread_id, message_id, to, subject, body)
            .await?;
        Ok(format!("Reply sent (ID: {msg_id})\nTo: {to}"))
    }
}
