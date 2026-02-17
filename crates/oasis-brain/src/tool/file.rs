use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use serde_json::json;

use crate::tool::{Tool, ToolResult};

const MAX_WRITE_SIZE: usize = 1_048_576; // 1MB
const MAX_READ_SIZE: usize = 1_048_576; // 1MB

/// File extensions considered sensitive — blocked even inside workspace.
const SENSITIVE_EXTENSIONS: &[&str] = &[".env", ".key", ".pem", ".p12", ".pfx", ".jks"];

pub struct FileTool {
    workspace: String,
}

impl FileTool {
    pub fn new(workspace: String) -> Self {
        Self { workspace }
    }

    /// Resolve a path to an absolute path within the workspace.
    /// Returns Err if the path escapes the workspace root.
    fn resolve_path(&self, path: &str) -> Result<std::path::PathBuf, String> {
        let workspace = std::path::Path::new(&self.workspace);

        // Workspace must exist for safe path resolution
        let canonical_workspace = workspace
            .canonicalize()
            .map_err(|_| "Workspace directory does not exist".to_string())?;

        // Reject absolute paths that don't start with the workspace
        if std::path::Path::new(path).is_absolute() {
            let abs = std::path::PathBuf::from(path);
            if !abs.starts_with(&canonical_workspace) {
                return Err("Access denied: path escapes workspace root".to_string());
            }
        }

        let resolved = canonical_workspace.join(path);

        // For existing paths, canonicalize and check containment.
        // For new paths, canonicalize the parent and check containment.
        let canonical = if resolved.exists() {
            resolved.canonicalize().map_err(|e| format!("Invalid path: {e}"))?
        } else {
            // Canonicalize the parent, then append the filename
            let parent = resolved.parent().unwrap_or(&canonical_workspace);
            if !parent.exists() {
                // Try to resolve what we can — walk components from workspace root
                let mut check = canonical_workspace.clone();
                let rel = resolved.strip_prefix(&canonical_workspace).unwrap_or(&resolved);
                for component in rel.components() {
                    check.push(component);
                    if check.exists() {
                        check = check.canonicalize().map_err(|e| format!("Invalid path: {e}"))?;
                    }
                }
                check
            } else {
                let canonical_parent = parent.canonicalize().map_err(|e| format!("Invalid path: {e}"))?;
                let filename = resolved.file_name().ok_or("Invalid filename")?;
                canonical_parent.join(filename)
            }
        };

        if !canonical.starts_with(&canonical_workspace) {
            return Err("Access denied: path escapes workspace root".to_string());
        }

        // Check sensitive file extensions
        if let Some(name) = canonical.file_name().and_then(|n| n.to_str()) {
            let lower = name.to_lowercase();
            for ext in SENSITIVE_EXTENSIONS {
                if lower.ends_with(ext) {
                    return Err(format!("Access denied: sensitive file type ({ext})"));
                }
            }
        }

        Ok(canonical)
    }
}

#[async_trait]
impl Tool for FileTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![
            ToolDefinition {
                name: "file_read".to_string(),
                description: "Read the contents of a file in the workspace.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "path": { "type": "string", "description": "File path (relative to workspace root)" }
                    },
                    "required": ["path"]
                }),
            },
            ToolDefinition {
                name: "file_write".to_string(),
                description: "Write content to a file in the workspace. Creates parent directories if needed.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "path": { "type": "string", "description": "File path (relative to workspace root)" },
                        "content": { "type": "string", "description": "Content to write" }
                    },
                    "required": ["path", "content"]
                }),
            },
            ToolDefinition {
                name: "file_list".to_string(),
                description: "List files and directories in a workspace path.".to_string(),
                parameters: json!({
                    "type": "object",
                    "properties": {
                        "path": { "type": "string", "description": "Directory path (relative to workspace root). Defaults to workspace root." }
                    }
                }),
            },
        ]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        match name {
            "file_read" => {
                let path = match args["path"].as_str() {
                    Some(p) => p,
                    None => return ToolResult::err("Missing required parameter: path"),
                };
                let resolved = match self.resolve_path(path) {
                    Ok(p) => p,
                    Err(e) => return ToolResult::err(e),
                };
                match tokio::fs::read_to_string(&resolved).await {
                    Ok(content) => {
                        if content.len() > MAX_READ_SIZE {
                            ToolResult::ok(format!(
                                "{}...\n(truncated, {} bytes total)",
                                &content[..MAX_READ_SIZE],
                                content.len()
                            ))
                        } else {
                            ToolResult::ok(content)
                        }
                    }
                    Err(e) => ToolResult::err(format!("Failed to read file: {e}")),
                }
            }
            "file_write" => {
                let path = match args["path"].as_str() {
                    Some(p) => p,
                    None => return ToolResult::err("Missing required parameter: path"),
                };
                let content = match args["content"].as_str() {
                    Some(c) => c,
                    None => return ToolResult::err("Missing required parameter: content"),
                };
                if content.len() > MAX_WRITE_SIZE {
                    return ToolResult::err(format!(
                        "Content too large: {} bytes (max {})",
                        content.len(),
                        MAX_WRITE_SIZE
                    ));
                }
                let resolved = match self.resolve_path(path) {
                    Ok(p) => p,
                    Err(e) => return ToolResult::err(e),
                };
                // Create parent directories
                if let Some(parent) = resolved.parent() {
                    let _ = tokio::fs::create_dir_all(parent).await;
                }
                match tokio::fs::write(&resolved, content).await {
                    Ok(()) => ToolResult::ok(format!(
                        "Written {} bytes to {}",
                        content.len(),
                        resolved.display()
                    )),
                    Err(e) => ToolResult::err(format!("Failed to write file: {e}")),
                }
            }
            "file_list" => {
                let path = args["path"].as_str().unwrap_or(".");
                let resolved = match self.resolve_path(path) {
                    Ok(p) => p,
                    Err(e) => return ToolResult::err(e),
                };
                let mut entries = match tokio::fs::read_dir(&resolved).await {
                    Ok(rd) => rd,
                    Err(e) => return ToolResult::err(format!("Failed to list directory: {e}")),
                };
                let mut output = Vec::new();
                while let Ok(Some(entry)) = entries.next_entry().await {
                    let name = entry.file_name().to_string_lossy().to_string();
                    let is_dir = entry.file_type().await.map(|t| t.is_dir()).unwrap_or(false);
                    if is_dir {
                        output.push(format!("{name}/"));
                    } else {
                        output.push(name);
                    }
                }
                output.sort();
                if output.is_empty() {
                    ToolResult::ok("(empty directory)")
                } else {
                    ToolResult::ok(output.join("\n"))
                }
            }
            _ => ToolResult::err(format!("Unknown file tool: {name}")),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn setup_workspace() -> std::path::PathBuf {
        let dir = std::env::temp_dir().join("oasis-file-tool-test");
        let _ = std::fs::create_dir_all(&dir);
        dir
    }

    #[test]
    fn test_path_traversal_blocked() {
        let ws = setup_workspace();
        let tool = FileTool::new(ws.to_string_lossy().to_string());
        assert!(tool.resolve_path("../../etc/passwd").is_err());
        assert!(tool.resolve_path("/etc/passwd").is_err());
    }

    #[test]
    fn test_sensitive_files_blocked() {
        let ws = setup_workspace();
        let tool = FileTool::new(ws.to_string_lossy().to_string());
        assert!(tool.resolve_path("config.env").is_err());
        assert!(tool.resolve_path("server.key").is_err());
        assert!(tool.resolve_path("cert.pem").is_err());
    }

    #[test]
    fn test_valid_paths_allowed() {
        let ws = setup_workspace();
        let tool = FileTool::new(ws.to_string_lossy().to_string());
        let result = tool.resolve_path("myapp/src/main.rs");
        // Either Ok (path resolves within workspace) or non-security error
        if let Err(e) = result {
            assert!(!e.contains("Access denied"), "Should not be a security error: {e}");
        }
    }

    #[test]
    fn test_nonexistent_workspace_rejected() {
        let tool = FileTool::new("/nonexistent/workspace/path".to_string());
        assert!(tool.resolve_path("anything.txt").is_err());
    }
}
