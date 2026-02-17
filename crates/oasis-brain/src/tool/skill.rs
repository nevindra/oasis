use std::sync::Arc;

use async_trait::async_trait;
use oasis_core::types::*;
use serde_json::json;

use crate::service::llm::Embedder;
use crate::service::store::VectorStore;
use crate::tool::{Tool, ToolResult};

pub struct SkillTool {
    store: Arc<VectorStore>,
    embedder: Arc<Embedder>,
}

impl SkillTool {
    pub fn new(store: Arc<VectorStore>, embedder: Arc<Embedder>) -> Self {
        Self { store, embedder }
    }

    async fn embed_text(&self, text: &str) -> oasis_core::error::Result<Vec<f32>> {
        let vecs = self.embedder.embed(&[text]).await?;
        vecs.into_iter()
            .next()
            .ok_or_else(|| oasis_core::error::OasisError::Llm {
                provider: "embedding".to_string(),
                message: "empty embedding result".to_string(),
            })
    }
}

#[async_trait]
impl Tool for SkillTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![
            ToolDefinition {
                name: "skill_create".to_string(),
                description: "Create a new skill (reusable prompt package). A skill teaches Oasis how to handle a specific domain or workflow. The instructions should be detailed markdown explaining when and how to use this skill.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "name": { "type": "string", "description": "Short skill name (e.g. 'Express API Builder')" },
                        "description": { "type": "string", "description": "One-sentence description of what this skill does. Used for matching user requests to skills." },
                        "instructions": { "type": "string", "description": "Full markdown instructions: when to use, step-by-step workflow, tips, constraints." },
                        "tools": {
                            "type": "array",
                            "items": { "type": "string" },
                            "description": "List of tool names this skill is allowed to use (e.g. ['shell_exec', 'file_write']). Omit for all tools."
                        }
                    },
                    "required": ["name", "description", "instructions"]
                }),
            },
            ToolDefinition {
                name: "skill_list".to_string(),
                description: "List all available skills with their descriptions.".to_string(),
                parameters: json!({ "type": "object", "properties": {} }),
            },
            ToolDefinition {
                name: "skill_update".to_string(),
                description: "Update a skill's instructions or configuration. Match by name substring.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "name_query": { "type": "string", "description": "Substring to match the skill name" },
                        "name": { "type": "string", "description": "New name (optional)" },
                        "description": { "type": "string", "description": "New description (optional)" },
                        "instructions": { "type": "string", "description": "New instructions (optional)" },
                        "tools": {
                            "type": "array",
                            "items": { "type": "string" },
                            "description": "New tool allowlist (optional)"
                        }
                    },
                    "required": ["name_query"]
                }),
            },
            ToolDefinition {
                name: "skill_delete".to_string(),
                description: "Delete a skill by name substring.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "name_query": { "type": "string", "description": "Substring to match the skill name" }
                    },
                    "required": ["name_query"]
                }),
            },
            ToolDefinition {
                name: "skill_export".to_string(),
                description: "Export a skill as markdown with YAML frontmatter, suitable for sharing or backup.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "name_query": { "type": "string", "description": "Substring to match the skill name" }
                    },
                    "required": ["name_query"]
                }),
            },
            ToolDefinition {
                name: "skill_import".to_string(),
                description: "Import a skill from markdown with YAML frontmatter.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "markdown": { "type": "string", "description": "Full markdown content with YAML frontmatter (---\\nname: ...\\n---\\n# Instructions...)" }
                    },
                    "required": ["markdown"]
                }),
            },
        ]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        let result = match name {
            "skill_create" => {
                let skill_name = args["name"].as_str().unwrap_or("");
                let description = args["description"].as_str().unwrap_or("");
                let instructions = args["instructions"].as_str().unwrap_or("");
                let tools = args.get("tools").and_then(|v| {
                    v.as_array().map(|arr| {
                        serde_json::Value::Array(arr.clone()).to_string()
                    })
                });
                self.handle_create(skill_name, description, instructions, tools).await
            }
            "skill_list" => self.handle_list().await,
            "skill_update" => {
                let query = args["name_query"].as_str().unwrap_or("");
                self.handle_update(query, args).await
            }
            "skill_delete" => {
                let query = args["name_query"].as_str().unwrap_or("");
                self.handle_delete(query).await
            }
            "skill_export" => {
                let query = args["name_query"].as_str().unwrap_or("");
                self.handle_export(query).await
            }
            "skill_import" => {
                let markdown = args["markdown"].as_str().unwrap_or("");
                self.handle_import(markdown).await
            }
            _ => return ToolResult::err(format!("Unknown skill tool: {name}")),
        };
        match result {
            Ok(r) => ToolResult::ok(r),
            Err(e) => ToolResult::err(e.to_string()),
        }
    }
}

impl SkillTool {
    async fn handle_create(
        &self,
        name: &str,
        description: &str,
        instructions: &str,
        tools: Option<String>,
    ) -> oasis_core::error::Result<String> {
        let embedding = self.embed_text(description).await?;
        let now = now_unix();
        let skill = Skill {
            id: new_id(),
            name: name.to_string(),
            description: description.to_string(),
            instructions: instructions.to_string(),
            tools,
            model: None,
            created_at: now,
            updated_at: now,
        };
        self.store.insert_skill(&skill, &embedding).await?;
        Ok(format!("Skill created: **{}**\n{}", skill.name, skill.description))
    }

    async fn handle_list(&self) -> oasis_core::error::Result<String> {
        let skills = self.store.list_skills().await?;
        if skills.is_empty() {
            return Ok("No skills defined yet.".to_string());
        }
        let mut output = format!("{} skill(s):\n\n", skills.len());
        for (i, s) in skills.iter().enumerate() {
            let tools_str = s.tools.as_deref().unwrap_or("all");
            output.push_str(&format!(
                "{}. **{}** — {}\n   Tools: {}\n",
                i + 1, s.name, s.description, tools_str,
            ));
        }
        Ok(output)
    }

    async fn handle_update(
        &self,
        query: &str,
        args: &serde_json::Value,
    ) -> oasis_core::error::Result<String> {
        let matches = self.store.find_skill_by_name(query).await?;
        if matches.is_empty() {
            return Ok(format!("No skill matching \"{query}\"."));
        }
        if matches.len() > 1 {
            let names: Vec<_> = matches.iter().map(|s| s.name.as_str()).collect();
            return Ok(format!("Multiple matches: {}. Be more specific.", names.join(", ")));
        }

        let mut skill = matches.into_iter().next().unwrap();
        if let Some(n) = args["name"].as_str() { skill.name = n.to_string(); }
        if let Some(d) = args["description"].as_str() { skill.description = d.to_string(); }
        if let Some(i) = args["instructions"].as_str() { skill.instructions = i.to_string(); }
        if let Some(t) = args.get("tools").and_then(|v| v.as_array()) {
            skill.tools = Some(serde_json::Value::Array(t.clone()).to_string());
        }
        skill.updated_at = now_unix();

        let embedding = self.embed_text(&skill.description).await?;
        self.store.update_skill(&skill, &embedding).await?;
        Ok(format!("Updated skill: **{}**", skill.name))
    }

    async fn handle_delete(&self, query: &str) -> oasis_core::error::Result<String> {
        let matches = self.store.find_skill_by_name(query).await?;
        if matches.is_empty() {
            return Ok(format!("No skill matching \"{query}\"."));
        }
        if matches.len() > 1 {
            let names: Vec<_> = matches.iter().map(|s| s.name.as_str()).collect();
            return Ok(format!("Multiple matches: {}. Be more specific.", names.join(", ")));
        }
        let skill = &matches[0];
        self.store.delete_skill(&skill.id).await?;
        Ok(format!("Deleted skill: **{}**", skill.name))
    }

    async fn handle_export(&self, query: &str) -> oasis_core::error::Result<String> {
        let matches = self.store.find_skill_by_name(query).await?;
        if matches.is_empty() {
            return Ok(format!("No skill matching \"{query}\"."));
        }
        if matches.len() > 1 {
            let names: Vec<_> = matches.iter().map(|s| s.name.as_str()).collect();
            return Ok(format!("Multiple matches: {}. Be more specific.", names.join(", ")));
        }
        let s = &matches[0];
        let tools_str = s.tools.as_deref().unwrap_or("null");
        let model_str = s.model.as_deref().unwrap_or("null");
        Ok(format!(
            "---\nname: {}\ndescription: {}\ntools: {}\nmodel: {}\n---\n\n{}",
            s.name, s.description, tools_str, model_str, s.instructions
        ))
    }

    async fn handle_import(&self, markdown: &str) -> oasis_core::error::Result<String> {
        // Parse YAML frontmatter
        let parts: Vec<&str> = markdown.splitn(3, "---").collect();
        if parts.len() < 3 {
            return Ok("Invalid format: expected YAML frontmatter between --- delimiters.".to_string());
        }
        let frontmatter = parts[1].trim();
        let instructions = parts[2].trim();

        let mut name = String::new();
        let mut description = String::new();
        let mut tools: Option<String> = None;

        for line in frontmatter.lines() {
            if let Some(v) = line.strip_prefix("name:") {
                name = v.trim().to_string();
            } else if let Some(v) = line.strip_prefix("description:") {
                description = v.trim().to_string();
            } else if let Some(v) = line.strip_prefix("tools:") {
                let v = v.trim();
                if v != "null" {
                    tools = Some(v.to_string());
                }
            }
        }

        if name.is_empty() || description.is_empty() {
            return Ok("Invalid frontmatter: name and description are required.".to_string());
        }

        self.handle_create(&name, &description, instructions, tools).await
    }
}
