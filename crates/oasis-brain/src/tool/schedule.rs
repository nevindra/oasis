use std::sync::Arc;

use async_trait::async_trait;
use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;
use serde_json::json;

use crate::service::store::VectorStore;
use crate::util::{date_to_unix_days, format_due, unix_days_to_date};
use crate::tool::{Tool, ToolResult};

pub struct ScheduleTool {
    store: Arc<VectorStore>,
    tz_offset: i32,
}

impl ScheduleTool {
    pub fn new(store: Arc<VectorStore>, tz_offset: i32) -> Self {
        Self { store, tz_offset }
    }
}

#[async_trait]
impl Tool for ScheduleTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![
            ToolDefinition {
                name: "schedule_create".to_string(),
                description: "Create a scheduled/recurring action that runs automatically. Use when the user wants something done periodically (daily briefings, recurring searches, regular summaries).".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "description": { "type": "string", "description": "Human-readable description of what this scheduled action does" },
                        "time": { "type": "string", "description": "Time in HH:MM format (24-hour, user's local timezone)" },
                        "recurrence": { "type": "string", "enum": ["once", "daily", "custom", "weekly", "monthly"], "description": "How often to run. Use 'once' for one-time actions. Use 'custom' to pick specific days of the week." },
                        "day": { "type": "string", "description": "For weekly: single day name (monday/senin). For custom: comma-separated day names (e.g. 'senin,rabu,kamis' or 'monday,wednesday,friday'). For monthly: day number (1-31)." },
                        "tools": {
                            "type": "array",
                            "items": {
                                "type": "object",
                                "properties": {
                                    "tool": { "type": "string", "description": "Tool name: web_search, task_list, knowledge_search" },
                                    "params": { "type": "object", "description": "Parameters for the tool" }
                                },
                                "required": ["tool", "params"]
                            },
                            "description": "Tools to execute when the schedule fires"
                        },
                        "synthesis_prompt": { "type": "string", "description": "How to format/summarize results (e.g. 'Summarize in Indonesian, keep it brief')" }
                    },
                    "required": ["description", "time", "recurrence", "tools"]
                }),
            },
            ToolDefinition {
                name: "schedule_list".to_string(),
                description: "List all scheduled actions with their schedules, status, and next run time.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {}
                }),
            },
            ToolDefinition {
                name: "schedule_update".to_string(),
                description: "Update a scheduled action: enable/disable it or change its schedule.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "description_query": { "type": "string", "description": "Substring to match the scheduled action description" },
                        "enabled": { "type": "boolean", "description": "Set to true to enable, false to disable/pause" },
                        "time": { "type": "string", "description": "New time in HH:MM format (optional)" },
                        "recurrence": { "type": "string", "enum": ["once", "daily", "custom", "weekly", "monthly"], "description": "New recurrence (optional)" },
                        "day": { "type": "string", "description": "New day(s) — single for weekly, comma-separated for custom, number for monthly (optional)" }
                    },
                    "required": ["description_query"]
                }),
            },
            ToolDefinition {
                name: "schedule_delete".to_string(),
                description: "Delete a scheduled action. Matches by description substring, or '*' to delete all.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "description_query": { "type": "string", "description": "Substring to match the description, or '*' for all" }
                    },
                    "required": ["description_query"]
                }),
            },
        ]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        let result = match name {
            "schedule_create" => {
                let description = args["description"].as_str().unwrap_or("");
                let time = args["time"].as_str().unwrap_or("08:00");
                let recurrence = args["recurrence"].as_str().unwrap_or("daily");
                let day = args["day"].as_str();
                let tools = &args["tools"];
                let synthesis_prompt = args["synthesis_prompt"].as_str();
                self.handle_create(description, time, recurrence, day, tools, synthesis_prompt).await
            }
            "schedule_list" => self.handle_list().await,
            "schedule_update" => {
                let query = args["description_query"].as_str().unwrap_or("");
                let enabled = args["enabled"].as_bool();
                let time = args["time"].as_str();
                let recurrence = args["recurrence"].as_str();
                let day = args["day"].as_str();
                self.handle_update(query, enabled, time, recurrence, day).await
            }
            "schedule_delete" => {
                let query = args["description_query"].as_str().unwrap_or("");
                self.handle_delete(query).await
            }
            _ => return ToolResult::err(format!("Unknown schedule tool: {name}")),
        };
        match result {
            Ok(r) => ToolResult::ok(r),
            Err(e) => ToolResult::err(e.to_string()),
        }
    }
}

impl ScheduleTool {
    async fn handle_create(
        &self,
        description: &str,
        time: &str,
        recurrence: &str,
        day: Option<&str>,
        tools: &serde_json::Value,
        synthesis_prompt: Option<&str>,
    ) -> Result<String> {
        let schedule = match recurrence {
            "once" => format!("{time} once"),
            "custom" => {
                let d = day.unwrap_or("monday,wednesday,friday");
                let normalized = normalize_day_list(d);
                format!("{time} custom({normalized})")
            }
            "weekly" => {
                let d = day.unwrap_or("monday");
                format!("{time} weekly({d})")
            }
            "monthly" => {
                let d = day.unwrap_or("1");
                format!("{time} monthly({d})")
            }
            _ => format!("{time} daily"),
        };

        let now = now_unix();
        let next_run = compute_next_run(&schedule, now, self.tz_offset)
            .ok_or_else(|| OasisError::Config("invalid schedule format".to_string()))?;

        let action = ScheduledAction {
            id: new_id(),
            description: description.to_string(),
            schedule: schedule.clone(),
            tool_calls: normalize_tool_calls(tools),
            synthesis_prompt: synthesis_prompt.map(|s| s.to_string()),
            enabled: true,
            last_run: None,
            next_run,
            created_at: now,
        };

        self.store.insert_scheduled_action(&action).await?;

        let next_run_str = format_due(next_run, self.tz_offset);
        Ok(format!(
            "Scheduled: {description}\nSchedule: {schedule}\nNext run: {next_run_str}"
        ))
    }

    async fn handle_list(&self) -> Result<String> {
        let actions = self.store.list_scheduled_actions().await?;
        if actions.is_empty() {
            return Ok("No scheduled actions.".to_string());
        }

        let mut output = format!("{} scheduled action(s):\n\n", actions.len());
        for (i, a) in actions.iter().enumerate() {
            let status = if a.enabled { "active" } else { "paused" };
            let next = format_due(a.next_run, self.tz_offset);
            output.push_str(&format!(
                "{}. {} [{}]\n   Schedule: {} | Next: {}\n",
                i + 1,
                a.description,
                status,
                a.schedule,
                next,
            ));
        }
        Ok(output)
    }

    async fn handle_update(
        &self,
        query: &str,
        enabled: Option<bool>,
        time: Option<&str>,
        recurrence: Option<&str>,
        day: Option<&str>,
    ) -> Result<String> {
        let matches = self.store.find_scheduled_action_by_description(query).await?;
        if matches.is_empty() {
            return Ok(format!("No scheduled action matching \"{query}\"."));
        }
        if matches.len() > 1 {
            let names: Vec<_> = matches.iter().map(|a| a.description.as_str()).collect();
            return Ok(format!(
                "Multiple matches: {}. Be more specific.",
                names.join(", ")
            ));
        }

        let action = &matches[0];
        let mut changes = Vec::new();

        if let Some(en) = enabled {
            self.store.update_scheduled_action_enabled(&action.id, en).await?;
            changes.push(if en { "enabled" } else { "paused" });
        }

        if time.is_some() || recurrence.is_some() {
            let current_parts: Vec<&str> = action.schedule.splitn(2, ' ').collect();
            let new_time = time.unwrap_or(current_parts.first().copied().unwrap_or("08:00"));
            let new_rec = recurrence
                .map(|r| match r {
                    "once" => "once".to_string(),
                    "custom" => {
                        let d = day.unwrap_or("monday,wednesday,friday");
                        let normalized = normalize_day_list(d);
                        format!("custom({normalized})")
                    }
                    "weekly" => {
                        let d = day.unwrap_or("monday");
                        format!("weekly({d})")
                    }
                    "monthly" => {
                        let d = day.unwrap_or("1");
                        format!("monthly({d})")
                    }
                    _ => "daily".to_string(),
                })
                .unwrap_or_else(|| current_parts.get(1).copied().unwrap_or("daily").to_string());

            let new_schedule = format!("{new_time} {new_rec}");
            let now = now_unix();
            let next_run = compute_next_run(&new_schedule, now, self.tz_offset)
                .ok_or_else(|| OasisError::Config("invalid schedule".to_string()))?;
            self.store
                .update_scheduled_action_schedule(&action.id, &new_schedule, next_run)
                .await?;
            changes.push("schedule updated");
        }

        if changes.is_empty() {
            return Ok("No changes specified.".to_string());
        }

        Ok(format!(
            "Updated \"{}\": {}",
            action.description,
            changes.join(", ")
        ))
    }

    async fn handle_delete(&self, query: &str) -> Result<String> {
        if query == "*" {
            let count = self.store.delete_all_scheduled_actions().await?;
            return Ok(format!("Deleted all {count} scheduled action(s)."));
        }

        let matches = self.store.find_scheduled_action_by_description(query).await?;
        if matches.is_empty() {
            return Ok(format!("No scheduled action matching \"{query}\"."));
        }

        let mut deleted = 0;
        for a in &matches {
            self.store.delete_scheduled_action(&a.id).await?;
            deleted += 1;
        }

        if deleted == 1 {
            Ok(format!("Deleted: {}", matches[0].description))
        } else {
            Ok(format!("Deleted {deleted} scheduled action(s)."))
        }
    }
}

// ─── Schedule utility functions ───────────────────────────────────────

/// Normalize tool_calls JSON: if the LLM produced string-encoded objects,
/// parse and re-serialize as proper objects.
fn normalize_tool_calls(tools: &serde_json::Value) -> String {
    if let Some(arr) = tools.as_array() {
        if arr.iter().all(|v| v.is_string()) {
            let parsed: Option<Vec<serde_json::Value>> = arr
                .iter()
                .map(|v| serde_json::from_str(v.as_str().unwrap_or("")))
                .collect::<std::result::Result<Vec<_>, _>>()
                .ok();
            if let Some(objects) = parsed {
                return serde_json::Value::Array(objects).to_string();
            }
        }
    }
    tools.to_string()
}

/// Compute the next run time (UTC) for a schedule string.
pub fn compute_next_run(schedule: &str, now: i64, tz_offset: i32) -> Option<i64> {
    let parts: Vec<&str> = schedule.splitn(2, ' ').collect();
    if parts.len() != 2 {
        return None;
    }

    let time_parts: Vec<&str> = parts[0].split(':').collect();
    if time_parts.len() != 2 {
        return None;
    }
    let hour: i64 = time_parts[0].parse().ok()?;
    let minute: i64 = time_parts[1].parse().ok()?;
    if hour > 23 || minute > 59 {
        return None;
    }

    let offset_secs = (tz_offset as i64) * 3600;
    let local_now = now + offset_secs;
    let local_days = local_now / 86400;
    let local_time_of_day = local_now % 86400;
    let target_time_of_day = hour * 3600 + minute * 60;

    let recurrence = parts[1].trim();

    match recurrence {
        "once" | "daily" => {
            let target_day = if local_time_of_day >= target_time_of_day {
                local_days + 1
            } else {
                local_days
            };
            let local_ts = target_day * 86400 + target_time_of_day;
            Some(local_ts - offset_secs)
        }
        s if s.starts_with("custom(") => {
            let days_str = s.trim_start_matches("custom(").trim_end_matches(')');
            let current_dow = ((local_days % 7) + 3) % 7;
            let mut best_ahead: Option<i64> = None;

            for day_name in days_str.split(',') {
                let target_dow = day_name_to_dow(day_name.trim())?;
                let mut ahead = target_dow - current_dow;
                if ahead < 0 {
                    ahead += 7;
                }
                if ahead == 0 && local_time_of_day >= target_time_of_day {
                    ahead = 7;
                }
                best_ahead = Some(match best_ahead {
                    Some(b) if b <= ahead => b,
                    _ => ahead,
                });
            }

            let days_ahead = best_ahead?;
            let target_day = local_days + days_ahead;
            let local_ts = target_day * 86400 + target_time_of_day;
            Some(local_ts - offset_secs)
        }
        s if s.starts_with("weekly(") => {
            let day_name = s.trim_start_matches("weekly(").trim_end_matches(')');
            let target_dow = day_name_to_dow(day_name)?;
            let current_dow = ((local_days % 7) + 3) % 7;
            let mut days_ahead = target_dow - current_dow;
            if days_ahead < 0 {
                days_ahead += 7;
            }
            if days_ahead == 0 && local_time_of_day >= target_time_of_day {
                days_ahead = 7;
            }
            let target_day = local_days + days_ahead;
            let local_ts = target_day * 86400 + target_time_of_day;
            Some(local_ts - offset_secs)
        }
        s if s.starts_with("monthly(") => {
            let dom_str = s.trim_start_matches("monthly(").trim_end_matches(')');
            let target_dom: i64 = dom_str.parse().ok()?;
            if target_dom < 1 || target_dom > 31 {
                return None;
            }
            let (y, m, d) = unix_days_to_date(local_days);
            let (target_y, target_m) =
                if d > target_dom || (d == target_dom && local_time_of_day >= target_time_of_day) {
                    if m == 12 {
                        (y + 1, 1)
                    } else {
                        (y, m + 1)
                    }
                } else {
                    (y, m)
                };
            let target_days = date_to_unix_days(target_y, target_m, target_dom);
            let local_ts = target_days * 86400 + target_time_of_day;
            Some(local_ts - offset_secs)
        }
        _ => None,
    }
}

/// Normalize comma-separated day names to lowercase, trimmed.
fn normalize_day_list(input: &str) -> String {
    input
        .split(',')
        .map(|d| d.trim().to_lowercase())
        .collect::<Vec<_>>()
        .join(",")
}

fn day_name_to_dow(name: &str) -> Option<i64> {
    match name.to_lowercase().as_str() {
        "monday" | "mon" | "senin" => Some(0),
        "tuesday" | "tue" | "selasa" => Some(1),
        "wednesday" | "wed" | "rabu" => Some(2),
        "thursday" | "thu" | "kamis" => Some(3),
        "friday" | "fri" | "jumat" => Some(4),
        "saturday" | "sat" | "sabtu" => Some(5),
        "sunday" | "sun" | "minggu" => Some(6),
        _ => None,
    }
}
