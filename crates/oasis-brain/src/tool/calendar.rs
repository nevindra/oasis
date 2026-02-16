use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use oasis_integrations::google::calendar::CalendarClient;
use oasis_integrations::google::GoogleAuth;
use serde_json::json;
use std::sync::Arc;

use crate::tool::{Tool, ToolResult};

pub struct CalendarTool {
    client: Arc<CalendarClient>,
    auth: Arc<GoogleAuth>,
}

impl CalendarTool {
    pub fn new(client: Arc<CalendarClient>, auth: Arc<GoogleAuth>) -> Self {
        Self { client, auth }
    }
}

#[async_trait]
impl Tool for CalendarTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![
            ToolDefinition {
                name: "google_connect".to_string(),
                description: "Get the Google OAuth authorization URL. Use when the user wants to connect their Google account.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {}
                }),
            },
            ToolDefinition {
                name: "calendar_list_events".to_string(),
                description: "List Google Calendar events for a date range. Returns events with title, time, and attendees.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "time_min": { "type": "string", "description": "Start of range in RFC3339 format (e.g. '2026-02-16T00:00:00+07:00')" },
                        "time_max": { "type": "string", "description": "End of range in RFC3339 format (e.g. '2026-02-17T00:00:00+07:00')" }
                    },
                    "required": ["time_min", "time_max"]
                }),
            },
            ToolDefinition {
                name: "calendar_create_event".to_string(),
                description: "Create a new Google Calendar event.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "summary": { "type": "string", "description": "Event title" },
                        "start": { "type": "string", "description": "Start time in RFC3339 format or YYYY-MM-DD for all-day" },
                        "end": { "type": "string", "description": "End time in RFC3339 format or YYYY-MM-DD for all-day" },
                        "description": { "type": "string", "description": "Event description" },
                        "attendees": {
                            "type": "array",
                            "items": { "type": "string" },
                            "description": "List of attendee email addresses"
                        }
                    },
                    "required": ["summary", "start", "end"]
                }),
            },
            ToolDefinition {
                name: "calendar_update_event".to_string(),
                description: "Update an existing Google Calendar event.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "event_id": { "type": "string", "description": "The event ID to update" },
                        "summary": { "type": "string", "description": "New event title" },
                        "start": { "type": "string", "description": "New start time" },
                        "end": { "type": "string", "description": "New end time" },
                        "description": { "type": "string", "description": "New description" }
                    },
                    "required": ["event_id"]
                }),
            },
            ToolDefinition {
                name: "calendar_delete_event".to_string(),
                description: "Delete a Google Calendar event. The LLM should confirm with the user via ask_user before calling this.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "event_id": { "type": "string", "description": "The event ID to delete" }
                    },
                    "required": ["event_id"]
                }),
            },
        ]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        let result = match name {
            "google_connect" => self.handle_connect().await,
            "calendar_list_events" => self.handle_list(args).await,
            "calendar_create_event" => self.handle_create(args).await,
            "calendar_update_event" => self.handle_update(args).await,
            "calendar_delete_event" => self.handle_delete(args).await,
            _ => return ToolResult::err(format!("Unknown calendar tool: {name}")),
        };
        match result {
            Ok(r) => ToolResult::ok(r),
            Err(e) => ToolResult::err(e.to_string()),
        }
    }
}

impl CalendarTool {
    async fn handle_connect(&self) -> oasis_core::error::Result<String> {
        if self.auth.is_connected().await {
            return Ok("Google account is already connected.".to_string());
        }
        let url = self.auth.auth_url();
        Ok(format!(
            "To connect your Google account, open this link:\n{url}\n\n\
             After authorizing, you'll see a \"Connected!\" confirmation page."
        ))
    }

    async fn handle_list(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let time_min = args["time_min"].as_str().unwrap_or("");
        let time_max = args["time_max"].as_str().unwrap_or("");

        let events = self.client.list_events(time_min, time_max).await?;

        if events.is_empty() {
            return Ok("No events found in that time range.".to_string());
        }

        let mut result = String::new();
        for event in &events {
            result.push_str(&format!(
                "- **{}**\n  {} — {}\n",
                event.summary, event.start, event.end
            ));
            if let Some(loc) = &event.location {
                result.push_str(&format!("  Location: {loc}\n"));
            }
            if !event.attendees.is_empty() {
                result.push_str(&format!("  Attendees: {}\n", event.attendees.join(", ")));
            }
            result.push_str(&format!("  ID: {}\n", event.id));
        }
        Ok(result.trim_end().to_string())
    }

    async fn handle_create(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let summary = args["summary"].as_str().unwrap_or("");
        let start = args["start"].as_str().unwrap_or("");
        let end = args["end"].as_str().unwrap_or("");
        let description = args["description"].as_str();
        let attendees: Vec<&str> = args["attendees"]
            .as_array()
            .map(|a| a.iter().filter_map(|v| v.as_str()).collect())
            .unwrap_or_default();

        let event = self
            .client
            .create_event(summary, start, end, description, &attendees)
            .await?;

        let mut result = format!("Created event: **{}**\n{} — {}", event.summary, event.start, event.end);
        if let Some(link) = &event.html_link {
            result.push_str(&format!("\nLink: {link}"));
        }
        Ok(result)
    }

    async fn handle_update(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let event_id = args["event_id"].as_str().unwrap_or("");
        let summary = args["summary"].as_str();
        let start = args["start"].as_str();
        let end = args["end"].as_str();
        let description = args["description"].as_str();

        let event = self
            .client
            .update_event(event_id, summary, start, end, description)
            .await?;

        Ok(format!(
            "Updated event: **{}**\n{} — {}",
            event.summary, event.start, event.end
        ))
    }

    async fn handle_delete(&self, args: &serde_json::Value) -> oasis_core::error::Result<String> {
        let event_id = args["event_id"].as_str().unwrap_or("");
        self.client.delete_event(event_id).await?;
        Ok(format!("Event {event_id} deleted."))
    }
}
