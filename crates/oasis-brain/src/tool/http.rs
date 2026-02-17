use async_trait::async_trait;
use oasis_core::types::ToolDefinition;
use serde_json::json;

use crate::tool::{Tool, ToolResult};

const MAX_RESPONSE_SIZE: usize = 5_242_880; // 5MB
const REQUEST_TIMEOUT_SECS: u64 = 30;

pub struct HttpTool;

impl HttpTool {
    pub fn new() -> Self {
        Self
    }

    /// Check if a URL targets an internal/private network address.
    fn is_internal_url(url: &str) -> bool {
        // Extract host from URL
        let host = url
            .strip_prefix("https://")
            .or_else(|| url.strip_prefix("http://"))
            .unwrap_or(url)
            .split('/')
            .next()
            .unwrap_or("")
            .split(':')
            .next()
            .unwrap_or("");

        let lower = host.to_lowercase();

        // Block localhost
        if lower == "localhost" || lower == "127.0.0.1" || lower == "::1" || lower == "[::1]" {
            return true;
        }

        // Block private IP ranges
        if let Ok(ip) = host.parse::<std::net::Ipv4Addr>() {
            let octets = ip.octets();
            return octets[0] == 10 // 10.0.0.0/8
                || (octets[0] == 172 && (16..=31).contains(&octets[1])) // 172.16.0.0/12
                || (octets[0] == 192 && octets[1] == 168) // 192.168.0.0/16
                || (octets[0] == 169 && octets[1] == 254) // 169.254.0.0/16 (link-local)
                || octets[0] == 0; // 0.0.0.0/8
        }

        false
    }
}

#[async_trait]
impl Tool for HttpTool {
    fn definitions(&self) -> Vec<ToolDefinition> {
        vec![ToolDefinition {
            name: "http_request".to_string(),
            description: "Make an HTTP request to an external URL. Supports GET, POST, PUT, DELETE. Use for API calls, webhooks, etc.".to_string(),
            parameters: json!({
                "type": "object",
                "properties": {
                    "url": { "type": "string", "description": "The URL to request" },
                    "method": { "type": "string", "enum": ["GET", "POST", "PUT", "DELETE", "PATCH"], "description": "HTTP method (default: GET)" },
                    "headers": { "type": "object", "description": "Optional HTTP headers as key-value pairs" },
                    "body": { "type": "string", "description": "Optional request body (for POST/PUT/PATCH)" }
                },
                "required": ["url"]
            }),
        }]
    }

    async fn execute(&self, name: &str, args: &serde_json::Value) -> ToolResult {
        if name != "http_request" {
            return ToolResult::err(format!("Unknown http tool: {name}"));
        }

        let url = match args["url"].as_str() {
            Some(u) if !u.is_empty() => u,
            _ => return ToolResult::err("Missing required parameter: url"),
        };

        if Self::is_internal_url(url) {
            return ToolResult::err("Blocked: cannot make requests to internal/private network addresses");
        }

        let method = args["method"].as_str().unwrap_or("GET").to_uppercase();

        let client = reqwest::Client::builder()
            .timeout(std::time::Duration::from_secs(REQUEST_TIMEOUT_SECS))
            .build()
            .unwrap_or_default();

        let mut request = match method.as_str() {
            "GET" => client.get(url),
            "POST" => client.post(url),
            "PUT" => client.put(url),
            "DELETE" => client.delete(url),
            "PATCH" => client.patch(url),
            _ => return ToolResult::err(format!("Unsupported HTTP method: {method}")),
        };

        // Add headers
        if let Some(headers) = args["headers"].as_object() {
            for (key, value) in headers {
                if let Some(v) = value.as_str() {
                    request = request.header(key.as_str(), v);
                }
            }
        }

        // Add body
        if let Some(body) = args["body"].as_str() {
            request = request.body(body.to_string());
        }

        match request.send().await {
            Ok(response) => {
                let status = response.status();
                let headers_str = response.headers().iter()
                    .take(20)
                    .map(|(k, v)| format!("{}: {}", k, v.to_str().unwrap_or("?")))
                    .collect::<Vec<_>>()
                    .join("\n");

                match response.bytes().await {
                    Ok(bytes) => {
                        if bytes.len() > MAX_RESPONSE_SIZE {
                            ToolResult::ok(format!(
                                "HTTP {} {}\n{}\n\n(response too large: {} bytes, max {})",
                                status.as_u16(), method, headers_str, bytes.len(), MAX_RESPONSE_SIZE
                            ))
                        } else {
                            let body = String::from_utf8_lossy(&bytes);
                            let display_body = if body.len() > 50_000 {
                                format!("{}...\n(truncated, {} bytes total)", &body[..50_000], body.len())
                            } else {
                                body.to_string()
                            };
                            ToolResult::ok(format!(
                                "HTTP {} {}\n{}\n\n{}",
                                status.as_u16(), method, headers_str, display_body
                            ))
                        }
                    }
                    Err(e) => ToolResult::err(format!("Failed to read response body: {e}")),
                }
            }
            Err(e) => ToolResult::err(format!("HTTP request failed: {e}")),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_internal_urls_blocked() {
        assert!(HttpTool::is_internal_url("http://localhost:3000/api"));
        assert!(HttpTool::is_internal_url("http://127.0.0.1/api"));
        assert!(HttpTool::is_internal_url("http://10.0.0.1/api"));
        assert!(HttpTool::is_internal_url("http://172.16.0.1/api"));
        assert!(HttpTool::is_internal_url("http://192.168.1.1/api"));
        assert!(HttpTool::is_internal_url("http://169.254.169.254/metadata"));
    }

    #[test]
    fn test_external_urls_allowed() {
        assert!(!HttpTool::is_internal_url("https://api.example.com/v1/data"));
        assert!(!HttpTool::is_internal_url("https://github.com"));
        assert!(!HttpTool::is_internal_url("http://8.8.8.8"));
    }
}
