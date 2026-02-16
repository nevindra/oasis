use oasis_core::types::ToolDefinition;
use serde_json::json;

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

/// Build tool definitions for all available built-in tools.
pub fn builtin_tool_definitions() -> Vec<ToolDefinition> {
    vec![
        task_create_def(),
        task_list_def(),
        task_update_def(),
        task_delete_def(),
        web_search_def(),
        knowledge_search_def(),
        remember_def(),
        schedule_create_def(),
        schedule_list_def(),
        schedule_update_def(),
        schedule_delete_def(),
        browse_url_def(),
        page_click_def(),
        page_type_def(),
        page_read_def(),
    ]
}

fn task_create_def() -> ToolDefinition {
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
    }
}

fn task_list_def() -> ToolDefinition {
    ToolDefinition {
        name: "task_list".to_string(),
        description: "List tasks. Can filter by status (todo, in_progress, done). Use when the user asks about their tasks.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "status": { "type": "string", "enum": ["todo", "in_progress", "done"], "description": "Filter by status" }
            }
        }),
    }
}

fn task_update_def() -> ToolDefinition {
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
    }
}

fn task_delete_def() -> ToolDefinition {
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
    }
}

fn web_search_def() -> ToolDefinition {
    ToolDefinition {
        name: "web_search".to_string(),
        description: "Search the web for current/real-time information. Use for recent events, news, prices, weather, or anything that requires up-to-date data.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "query": { "type": "string", "description": "Search query optimized for search engines" }
            },
            "required": ["query"]
        }),
    }
}

fn knowledge_search_def() -> ToolDefinition {
    ToolDefinition {
        name: "knowledge_search".to_string(),
        description: "Search the user's personal knowledge base for previously saved information, documents, and past conversations.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "query": { "type": "string", "description": "Search query" }
            },
            "required": ["query"]
        }),
    }
}

fn remember_def() -> ToolDefinition {
    ToolDefinition {
        name: "remember".to_string(),
        description: "Save information to the user's knowledge base. Use when the user explicitly asks to remember or save something.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "content": { "type": "string", "description": "The content to save" }
            },
            "required": ["content"]
        }),
    }
}

fn schedule_create_def() -> ToolDefinition {
    ToolDefinition {
        name: "schedule_create".to_string(),
        description: "Create a scheduled/recurring action that runs automatically. Use when the user wants something done periodically (daily briefings, recurring searches, regular summaries).".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "description": { "type": "string", "description": "Human-readable description of what this scheduled action does" },
                "time": { "type": "string", "description": "Time in HH:MM format (24-hour, user's local timezone)" },
                "recurrence": { "type": "string", "enum": ["daily", "weekly", "monthly"], "description": "How often to run" },
                "day": { "type": "string", "description": "Day for weekly (monday-sunday/senin-minggu) or day number for monthly (1-31). Required for weekly/monthly." },
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
    }
}

fn schedule_list_def() -> ToolDefinition {
    ToolDefinition {
        name: "schedule_list".to_string(),
        description: "List all scheduled actions with their schedules, status, and next run time.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {}
        }),
    }
}

fn schedule_update_def() -> ToolDefinition {
    ToolDefinition {
        name: "schedule_update".to_string(),
        description: "Update a scheduled action: enable/disable it or change its schedule.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "description_query": { "type": "string", "description": "Substring to match the scheduled action description" },
                "enabled": { "type": "boolean", "description": "Set to true to enable, false to disable/pause" },
                "time": { "type": "string", "description": "New time in HH:MM format (optional)" },
                "recurrence": { "type": "string", "enum": ["daily", "weekly", "monthly"], "description": "New recurrence (optional)" },
                "day": { "type": "string", "description": "New day for weekly/monthly (optional)" }
            },
            "required": ["description_query"]
        }),
    }
}

fn schedule_delete_def() -> ToolDefinition {
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
    }
}

fn browse_url_def() -> ToolDefinition {
    ToolDefinition {
        name: "browse_url".to_string(),
        description: "Open a URL in a browser and return the page content with interactive elements. Use to interact with web pages, fill forms, check prices, etc.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "url": { "type": "string", "description": "The URL to navigate to" }
            },
            "required": ["url"]
        }),
    }
}

fn page_click_def() -> ToolDefinition {
    ToolDefinition {
        name: "page_click".to_string(),
        description: "Click an interactive element on the current browser page by its number from the elements list. Use after browse_url.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "element": { "type": "string", "description": "Element number to click (e.g. '3' for element [3])" }
            },
            "required": ["element"]
        }),
    }
}

fn page_type_def() -> ToolDefinition {
    ToolDefinition {
        name: "page_type".to_string(),
        description: "Type text into an input field on the current browser page by its element number. Replaces existing text. Use after browse_url.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {
                "element": { "type": "string", "description": "Element number to type into (e.g. '1' for element [1])" },
                "text": { "type": "string", "description": "Text to type into the element" }
            },
            "required": ["element", "text"]
        }),
    }
}

fn page_read_def() -> ToolDefinition {
    ToolDefinition {
        name: "page_read".to_string(),
        description: "Read the current browser page content and interactive elements without any interaction. Use to refresh the view after waiting or to re-read the page.".to_string(),
        parameters: json!({
            "type": "object",
            "properties": {}
        }),
    }
}
