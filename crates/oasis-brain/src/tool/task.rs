use async_trait::async_trait;
use oasis_core::types::{Task, ToolDefinition};
use serde_json::json;

use std::sync::Arc;

use crate::service::tasks::TaskManager;
use crate::tool::{Tool, ToolResult};

pub struct TaskTool {
    tasks: Arc<TaskManager>,
    tz_offset: i32,
}

impl TaskTool {
    pub fn new(tasks: Arc<TaskManager>, tz_offset: i32) -> Self {
        Self { tasks, tz_offset }
    }
}

#[async_trait]
impl Tool for TaskTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![
            ToolDefinition {
                name: "task_create".to_string(),
                description: "Create a new task or todo item. Use when the user wants to track something to do.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "title": { "type": "string", "description": "Task title" },
                        "description": { "type": "string", "description": "Optional task description" },
                        "due": { "type": "string", "description": "Due date in YYYY-MM-DD or YYYY-MM-DDTHH:MM format (local time)" },
                        "priority": { "type": "string", "enum": ["low", "medium", "high"], "description": "Task priority" }
                    },
                    "required": ["title"]
                }),
            },
            ToolDefinition {
                name: "task_list".to_string(),
                description: "List tasks. Can filter by status (todo, in_progress, done). Use when the user asks about their tasks.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "status": { "type": "string", "enum": ["todo", "in_progress", "done"], "description": "Filter by status" }
                    }
                }),
            },
            ToolDefinition {
                name: "task_update".to_string(),
                description: "Update a task's status. Use when the user wants to mark a task as done, in progress, etc.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "title_query": { "type": "string", "description": "Substring to match the task title" },
                        "new_status": { "type": "string", "enum": ["todo", "in_progress", "done"], "description": "New status" }
                    },
                    "required": ["title_query", "new_status"]
                }),
            },
            ToolDefinition {
                name: "task_delete".to_string(),
                description: "Delete a task. Use when the user wants to remove a task entirely. Use title_query '*' to delete all tasks.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "title_query": { "type": "string", "description": "Substring to match the task title, or '*' for all" }
                    },
                    "required": ["title_query"]
                }),
            },
        ]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        let result = match name {
            "task_create" => {
                let title = args["title"].as_str().unwrap_or("");
                let description = args["description"].as_str();
                let due = args["due"].as_str();
                let priority = priority_to_int(args["priority"].as_str());
                self.handle_create(title, description, due, priority).await
            }
            "task_list" => {
                let status = args["status"].as_str();
                self.handle_query(status).await
            }
            "task_update" => {
                let title_query = args["title_query"].as_str().unwrap_or("");
                let new_status = args["new_status"].as_str().unwrap_or("done");
                self.handle_update(title_query, new_status).await
            }
            "task_delete" => {
                let title_query = args["title_query"].as_str().unwrap_or("");
                self.handle_delete(title_query).await
            }
            _ => return ToolResult::err(format!("Unknown task tool: {name}")),
        };
        match result {
            Ok(r) => ToolResult::ok(r),
            Err(e) => ToolResult::err(e.to_string()),
        }
    }
}

impl TaskTool {
    async fn handle_create(
        &self,
        title: &str,
        description: Option<&str>,
        due: Option<&str>,
        priority: i32,
    ) -> oasis_core::error::Result<String> {
        let due_at = due.and_then(|d| parse_date_to_unix(d, self.tz_offset));
        let task = self
            .tasks
            .create_task(title, None, None, description, priority, due_at)
            .await?;

        let short = &task.id[task.id.len().saturating_sub(6)..];
        let mut response = format!("Task created: **{}** (#{short})", task.title);
        if let Some(desc) = &task.description {
            response.push_str(&format!("\nDescription: {desc}"));
        }
        if let Some(due_ts) = task.due_at {
            response.push_str(&format!("\nDue: {}", format_due(due_ts, self.tz_offset)));
        }
        if task.priority > 0 {
            let prio_label = match task.priority {
                1 => "low",
                2 => "medium",
                3 => "high",
                _ => "normal",
            };
            response.push_str(&format!("\nPriority: {prio_label}"));
        }
        response.push_str(&format!("\nStatus: {}", task.status));
        Ok(response)
    }

    async fn handle_query(&self, filter: Option<&str>) -> oasis_core::error::Result<String> {
        let status_filter = filter.and_then(|f| {
            let lower = f.to_lowercase();
            if lower.contains("done") || lower.contains("complete") {
                Some("done")
            } else if lower.contains("progress") || lower.contains("active") || lower.contains("doing") {
                Some("in_progress")
            } else if lower.contains("todo") || lower.contains("pending") {
                Some("todo")
            } else {
                None
            }
        });

        if status_filter.is_none() {
            let summary = self.tasks.get_active_task_summary(self.tz_offset).await?;
            return Ok(summary);
        }

        let tasks = self.tasks.list_tasks(None, status_filter).await?;
        if tasks.is_empty() {
            return Ok(format!(
                "No tasks found with status: {}",
                status_filter.unwrap_or("any")
            ));
        }

        let mut response = String::new();
        for task in &tasks {
            let due_str = match task.due_at {
                Some(ts) => format!(" (due: {})", format_due(ts, self.tz_offset)),
                None => String::new(),
            };
            response.push_str(&format!("- [{}] {}{}\n", task.status, task.title, due_str));
        }
        Ok(response.trim_end().to_string())
    }

    async fn handle_update(
        &self,
        title_query: &str,
        new_status: &str,
    ) -> oasis_core::error::Result<String> {
        let matching_tasks = self.find_tasks_smart(title_query).await?;

        if matching_tasks.is_empty() {
            return Ok(format!(
                "No tasks found matching \"{title_query}\". Check the title and try again."
            ));
        }

        if matching_tasks.len() > 1 {
            let mut response = format!(
                "Found {} tasks matching \"{title_query}\". Please be more specific:\n",
                matching_tasks.len()
            );
            for task in &matching_tasks {
                let short = &task.id[task.id.len().saturating_sub(6)..];
                response.push_str(&format!("- [{}] #{} {}\n", task.status, short, task.title));
            }
            return Ok(response.trim_end().to_string());
        }

        let task = &matching_tasks[0];
        self.tasks.update_task_status(&task.id, new_status).await?;
        Ok(format!(
            "Updated \"{}\" from {} to **{}**",
            task.title, task.status, new_status
        ))
    }

    async fn handle_delete(&self, title_query: &str) -> oasis_core::error::Result<String> {
        if title_query.trim() == "*" {
            let count = self.tasks.delete_all_tasks().await?;
            return Ok(format!("Deleted all tasks ({count} total)."));
        }

        if title_query.is_empty() {
            return Ok("Please specify which task to delete.".to_string());
        }

        let matching_tasks = self.find_tasks_smart(title_query).await?;

        if matching_tasks.is_empty() {
            return Ok(format!(
                "No tasks found matching \"{title_query}\". Check the title and try again."
            ));
        }

        if matching_tasks.len() > 1 {
            let mut response = format!(
                "Found {} tasks matching \"{title_query}\". Please be more specific:\n",
                matching_tasks.len()
            );
            for task in &matching_tasks {
                let short = &task.id[task.id.len().saturating_sub(6)..];
                response.push_str(&format!("- [{}] #{} {}\n", task.status, short, task.title));
            }
            return Ok(response.trim_end().to_string());
        }

        let task = &matching_tasks[0];
        self.tasks.delete_task(&task.id).await?;
        Ok(format!("Deleted task: \"{}\"", task.title))
    }

    async fn find_tasks_smart(&self, query: &str) -> oasis_core::error::Result<Vec<Task>> {
        if let Some(short) = query.strip_prefix('#') {
            return self.tasks.find_task_by_short_id(short).await;
        }
        if query.len() >= 4 && query.len() <= 8 && query.chars().all(|c| c.is_ascii_hexdigit()) {
            let by_id = self.tasks.find_task_by_short_id(query).await?;
            if !by_id.is_empty() {
                return Ok(by_id);
            }
        }
        self.tasks.find_task_by_title(query).await
    }
}

// ─── Utility functions ────────────────────────────────────────────────

fn priority_to_int(priority: Option<&str>) -> i32 {
    match priority {
        Some("low") => 1,
        Some("medium") => 2,
        Some("high") => 3,
        _ => 0,
    }
}

pub fn format_due(ts: i64, tz_offset: i32) -> String {
    let local_ts = ts + (tz_offset as i64) * 3600;
    let days = local_ts / 86400;
    let remainder = local_ts % 86400;
    let (y, m, d) = unix_days_to_date(days);
    if (ts + (tz_offset as i64) * 3600) % 86400 == 0 {
        format!("{y:04}-{m:02}-{d:02}")
    } else {
        let h = remainder / 3600;
        let min = (remainder % 3600) / 60;
        format!("{y:04}-{m:02}-{d:02} {h:02}:{min:02}")
    }
}

pub fn parse_date_to_unix(date_str: &str, tz_offset: i32) -> Option<i64> {
    let offset_secs = (tz_offset as i64) * 3600;

    if let Some((date_part, time_part)) = date_str.split_once('T') {
        let date_parts: Vec<&str> = date_part.split('-').collect();
        let time_parts: Vec<&str> = time_part.split(':').collect();
        if date_parts.len() == 3 && time_parts.len() >= 2 {
            let year: i64 = date_parts[0].parse().ok()?;
            let month: i64 = date_parts[1].parse().ok()?;
            let day: i64 = date_parts[2].parse().ok()?;
            let hour: i64 = time_parts[0].parse().ok()?;
            let minute: i64 = time_parts[1].parse().ok()?;
            if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 {
                return None;
            }
            let local_ts = date_to_unix_days(year, month, day) * 86400 + hour * 3600 + minute * 60;
            return Some(local_ts - offset_secs);
        }
    }

    let parts: Vec<&str> = date_str.split('-').collect();
    if parts.len() != 3 {
        return None;
    }
    let year: i64 = parts[0].parse().ok()?;
    let month: i64 = parts[1].parse().ok()?;
    let day: i64 = parts[2].parse().ok()?;
    if month < 1 || month > 12 || day < 1 || day > 31 {
        return None;
    }
    Some(date_to_unix_days(year, month, day) * 86400 - offset_secs)
}

pub fn date_to_unix_days(year: i64, month: i64, day: i64) -> i64 {
    let y = if month <= 2 { year - 1 } else { year };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = (y - era * 400) as u64;
    let m = month;
    let doy = (153 * (if m > 2 { m - 3 } else { m + 9 }) + 2) / 5 + day - 1;
    let doe = yoe as i64 * 365 + yoe as i64 / 4 - yoe as i64 / 100 + doy;
    era * 146097 + doe - 719468
}

pub fn unix_days_to_date(days: i64) -> (i64, i64, i64) {
    let z = days + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = (z - era * 146097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    (y, m as i64, d as i64)
}
