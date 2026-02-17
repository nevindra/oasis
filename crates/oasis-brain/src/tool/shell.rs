use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use serde_json::json;

use crate::tool::{Tool, ToolResult};

pub struct ShellTool {
    workspace: String,
    timeout_secs: u64,
}

/// Patterns that indicate dangerous commands.
const BLOCKED_PATTERNS: &[&str] = &[
    "rm -rf /",
    "rm -rf /*",
    "mkfs",
    "dd if=",
    ":(){:|:&};:",
    "chmod -R 777 /",
    "chmod 777 /",
    "> /dev/sd",
    "fork()",
    "shutdown",
    "reboot",
    "init 0",
    "init 6",
    "halt",
    "poweroff",
];

/// Patterns that indicate background execution.
const BG_PATTERNS: &[&str] = &[" &", "nohup ", "disown", "setsid"];

impl ShellTool {
    pub fn new(workspace: String, timeout_secs: u64) -> Self {
        Self { workspace, timeout_secs }
    }

    fn validate_command(command: &str) -> Result<(), String> {
        let lower = command.to_lowercase();
        for pattern in BLOCKED_PATTERNS {
            if lower.contains(pattern) {
                return Err(format!("Blocked: command contains dangerous pattern '{pattern}'"));
            }
        }
        for pattern in BG_PATTERNS {
            if lower.contains(pattern) {
                return Err(format!("Blocked: background execution not allowed ('{pattern}')"));
            }
        }
        Ok(())
    }
}

#[async_trait]
impl Tool for ShellTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![ToolDefinition {
            name: "shell_exec".to_string(),
            description: "Execute a shell command in the workspace directory. Returns stdout and stderr. Use for running builds, scripts, package managers, git, etc.".to_string(),
            parameters: json!({
                "type": "object",
                "properties": {
                    "command": { "type": "string", "description": "The shell command to execute" }
                },
                "required": ["command"]
            }),
        }]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        if name != "shell_exec" {
            return ToolResult::err(format!("Unknown shell tool: {name}"));
        }

        let command = match args["command"].as_str() {
            Some(c) if !c.is_empty() => c,
            _ => return ToolResult::err("Missing required parameter: command"),
        };

        if let Err(e) = Self::validate_command(command) {
            return ToolResult::err(e);
        }

        // Ensure workspace directory exists
        let _ = tokio::fs::create_dir_all(&self.workspace).await;

        let timeout = std::time::Duration::from_secs(self.timeout_secs);

        let result = tokio::time::timeout(timeout, async {
            tokio::process::Command::new("sh")
                .arg("-c")
                .arg(command)
                .current_dir(&self.workspace)
                .output()
                .await
        })
        .await;

        match result {
            Ok(Ok(output)) => {
                let stdout = String::from_utf8_lossy(&output.stdout);
                let stderr = String::from_utf8_lossy(&output.stderr);
                let exit_code = output.status.code().unwrap_or(-1);

                let mut response = String::new();
                if !stdout.is_empty() {
                    let truncated = if stdout.len() > 50_000 {
                        format!("{}...\n(truncated, {} bytes total)", &stdout[..50_000], stdout.len())
                    } else {
                        stdout.to_string()
                    };
                    response.push_str(&truncated);
                }
                if !stderr.is_empty() {
                    if !response.is_empty() { response.push('\n'); }
                    let truncated = if stderr.len() > 10_000 {
                        format!("STDERR: {}...\n(truncated)", &stderr[..10_000])
                    } else {
                        format!("STDERR: {stderr}")
                    };
                    response.push_str(&truncated);
                }
                if response.is_empty() {
                    response = format!("(no output, exit code {exit_code})");
                } else if exit_code != 0 {
                    response.push_str(&format!("\n(exit code {exit_code})"));
                }
                ToolResult::ok(response)
            }
            Ok(Err(e)) => ToolResult::err(format!("Failed to execute command: {e}")),
            Err(_) => ToolResult::err(format!("Command timed out after {}s", self.timeout_secs)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_blocked_commands() {
        assert!(ShellTool::validate_command("rm -rf /").is_err());
        assert!(ShellTool::validate_command("rm -rf /*").is_err());
        assert!(ShellTool::validate_command("some; dd if=/dev/zero").is_err());
        assert!(ShellTool::validate_command("sleep 100 &").is_err());
        assert!(ShellTool::validate_command("nohup ./server").is_err());
    }

    #[test]
    fn test_allowed_commands() {
        assert!(ShellTool::validate_command("ls -la").is_ok());
        assert!(ShellTool::validate_command("npm install").is_ok());
        assert!(ShellTool::validate_command("cargo build").is_ok());
        assert!(ShellTool::validate_command("git status").is_ok());
        assert!(ShellTool::validate_command("rm -rf node_modules").is_ok());
    }
}
