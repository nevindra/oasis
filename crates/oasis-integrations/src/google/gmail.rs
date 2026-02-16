use std::sync::Arc;

use base64::Engine;
use oasis_core::error::{OasisError, Result};
use serde::{Deserialize, Serialize};

use super::GoogleAuth;

const GMAIL_API: &str = "https://gmail.googleapis.com/gmail/v1/users/me";

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MessageSummary {
    pub id: String,
    pub thread_id: String,
    pub subject: String,
    pub from: String,
    pub date: String,
    pub snippet: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MessageDetail {
    pub id: String,
    pub thread_id: String,
    pub subject: String,
    pub from: String,
    pub to: String,
    pub date: String,
    pub body: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Draft {
    pub id: String,
    pub message_id: String,
}

pub struct GmailClient {
    auth: Arc<GoogleAuth>,
    http: reqwest::Client,
}

impl GmailClient {
    pub fn new(auth: Arc<GoogleAuth>) -> Self {
        Self {
            auth,
            http: reqwest::Client::new(),
        }
    }

    async fn get(&self, url: &str) -> Result<serde_json::Value> {
        let token = self.auth.access_token().await?;
        let resp = self
            .http
            .get(url)
            .bearer_auth(&token)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("gmail request failed: {e}")))?;

        let status = resp.status().as_u16();
        let text = resp
            .text()
            .await
            .map_err(|e| OasisError::Integration(format!("gmail response read failed: {e}")))?;

        if status != 200 {
            return Err(OasisError::Http { status, body: text });
        }

        serde_json::from_str(&text)
            .map_err(|e| OasisError::Integration(format!("gmail json parse failed: {e}")))
    }

    async fn post_json(&self, url: &str, body: &serde_json::Value) -> Result<serde_json::Value> {
        let token = self.auth.access_token().await?;
        let resp = self
            .http
            .post(url)
            .bearer_auth(&token)
            .json(body)
            .send()
            .await
            .map_err(|e| OasisError::Integration(format!("gmail request failed: {e}")))?;

        let status = resp.status().as_u16();
        let text = resp
            .text()
            .await
            .map_err(|e| OasisError::Integration(format!("gmail response read failed: {e}")))?;

        if status != 200 {
            return Err(OasisError::Http { status, body: text });
        }

        serde_json::from_str(&text)
            .map_err(|e| OasisError::Integration(format!("gmail json parse failed: {e}")))
    }

    /// Search emails by Gmail query string (e.g. "from:alice subject:report").
    pub async fn search(&self, query: &str, max_results: u32) -> Result<Vec<MessageSummary>> {
        let url = format!(
            "{GMAIL_API}/messages?q={}&maxResults={max_results}",
            urlencod(query)
        );
        let data = self.get(&url).await?;
        let message_ids: Vec<String> = data["messages"]
            .as_array()
            .cloned()
            .unwrap_or_default()
            .iter()
            .filter_map(|m| m["id"].as_str().map(|s| s.to_string()))
            .collect();

        let mut summaries = Vec::new();
        for mid in message_ids.iter().take(max_results as usize) {
            let url = format!("{GMAIL_API}/messages/{mid}?format=metadata&metadataHeaders=Subject&metadataHeaders=From&metadataHeaders=Date");
            match self.get(&url).await {
                Ok(msg) => summaries.push(parse_summary(&msg)),
                Err(_) => continue,
            }
        }

        Ok(summaries)
    }

    /// Read the full content of a specific message.
    pub async fn read(&self, message_id: &str) -> Result<MessageDetail> {
        let url = format!("{GMAIL_API}/messages/{message_id}?format=full");
        let data = self.get(&url).await?;
        Ok(parse_detail(&data))
    }

    /// Create a draft email.
    pub async fn create_draft(
        &self,
        to: &str,
        subject: &str,
        body: &str,
    ) -> Result<Draft> {
        let raw = build_raw_message(to, subject, body);
        let url = format!("{GMAIL_API}/drafts");
        let payload = serde_json::json!({
            "message": {
                "raw": raw,
            }
        });

        let data = self.post_json(&url, &payload).await?;
        Ok(Draft {
            id: data["id"].as_str().unwrap_or_default().to_string(),
            message_id: data["message"]["id"]
                .as_str()
                .unwrap_or_default()
                .to_string(),
        })
    }

    /// Send an email directly.
    pub async fn send(&self, to: &str, subject: &str, body: &str) -> Result<String> {
        let raw = build_raw_message(to, subject, body);
        let url = format!("{GMAIL_API}/messages/send");
        let payload = serde_json::json!({ "raw": raw });

        let data = self.post_json(&url, &payload).await?;
        Ok(data["id"].as_str().unwrap_or_default().to_string())
    }

    /// Reply to a thread.
    pub async fn reply(
        &self,
        thread_id: &str,
        message_id: &str,
        to: &str,
        subject: &str,
        body: &str,
    ) -> Result<String> {
        let raw = build_raw_reply(to, subject, body, message_id);
        let url = format!("{GMAIL_API}/messages/send");
        let payload = serde_json::json!({
            "raw": raw,
            "threadId": thread_id,
        });

        let data = self.post_json(&url, &payload).await?;
        Ok(data["id"].as_str().unwrap_or_default().to_string())
    }
}

fn build_raw_message(to: &str, subject: &str, body: &str) -> String {
    let message = format!(
        "To: {to}\r\nSubject: {subject}\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n{body}"
    );
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(message.as_bytes())
}

fn build_raw_reply(to: &str, subject: &str, body: &str, in_reply_to: &str) -> String {
    let subject = if subject.starts_with("Re:") {
        subject.to_string()
    } else {
        format!("Re: {subject}")
    };
    let message = format!(
        "To: {to}\r\nSubject: {subject}\r\nIn-Reply-To: {in_reply_to}\r\n\
         References: {in_reply_to}\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n{body}"
    );
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(message.as_bytes())
}

fn parse_summary(v: &serde_json::Value) -> MessageSummary {
    let headers = v["payload"]["headers"]
        .as_array()
        .cloned()
        .unwrap_or_default();
    let get_header = |name: &str| -> String {
        headers
            .iter()
            .find(|h| h["name"].as_str().map(|n| n.eq_ignore_ascii_case(name)).unwrap_or(false))
            .and_then(|h| h["value"].as_str())
            .unwrap_or_default()
            .to_string()
    };

    MessageSummary {
        id: v["id"].as_str().unwrap_or_default().to_string(),
        thread_id: v["threadId"].as_str().unwrap_or_default().to_string(),
        subject: get_header("Subject"),
        from: get_header("From"),
        date: get_header("Date"),
        snippet: v["snippet"].as_str().unwrap_or_default().to_string(),
    }
}

fn parse_detail(v: &serde_json::Value) -> MessageDetail {
    let headers = v["payload"]["headers"]
        .as_array()
        .cloned()
        .unwrap_or_default();
    let get_header = |name: &str| -> String {
        headers
            .iter()
            .find(|h| h["name"].as_str().map(|n| n.eq_ignore_ascii_case(name)).unwrap_or(false))
            .and_then(|h| h["value"].as_str())
            .unwrap_or_default()
            .to_string()
    };

    // Try to extract body from parts or directly
    let body = extract_body(&v["payload"]);

    MessageDetail {
        id: v["id"].as_str().unwrap_or_default().to_string(),
        thread_id: v["threadId"].as_str().unwrap_or_default().to_string(),
        subject: get_header("Subject"),
        from: get_header("From"),
        to: get_header("To"),
        date: get_header("Date"),
        body,
    }
}

fn extract_body(payload: &serde_json::Value) -> String {
    // Try direct body data
    if let Some(data) = payload["body"]["data"].as_str() {
        if let Ok(bytes) = base64::engine::general_purpose::URL_SAFE_NO_PAD.decode(data) {
            if let Ok(text) = String::from_utf8(bytes) {
                return text;
            }
        }
    }

    // Try parts (multipart messages)
    if let Some(parts) = payload["parts"].as_array() {
        for part in parts {
            let mime = part["mimeType"].as_str().unwrap_or_default();
            if mime == "text/plain" {
                if let Some(data) = part["body"]["data"].as_str() {
                    if let Ok(bytes) =
                        base64::engine::general_purpose::URL_SAFE_NO_PAD.decode(data)
                    {
                        if let Ok(text) = String::from_utf8(bytes) {
                            return text;
                        }
                    }
                }
            }
        }
        // Fallback to text/html if no text/plain
        for part in parts {
            let mime = part["mimeType"].as_str().unwrap_or_default();
            if mime == "text/html" {
                if let Some(data) = part["body"]["data"].as_str() {
                    if let Ok(bytes) =
                        base64::engine::general_purpose::URL_SAFE_NO_PAD.decode(data)
                    {
                        if let Ok(text) = String::from_utf8(bytes) {
                            return text;
                        }
                    }
                }
            }
            // Recurse into nested parts
            if part["parts"].is_array() {
                let nested = extract_body(part);
                if !nested.is_empty() {
                    return nested;
                }
            }
        }
    }

    payload["snippet"].as_str().unwrap_or_default().to_string()
}

fn urlencod(s: &str) -> String {
    s.replace('%', "%25")
        .replace(' ', "%20")
        .replace('&', "%26")
        .replace('=', "%3D")
        .replace('+', "%2B")
        .replace('/', "%2F")
        .replace(':', "%3A")
        .replace('?', "%3F")
        .replace('#', "%23")
}
