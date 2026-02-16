use reqwest::Client;
use oasis_core::error::{OasisError, Result};
use crate::types::*;

const MAX_MESSAGE_LENGTH: usize = 4096;

pub struct TelegramBot {
    client: Client,
    token: String,
    base_url: String,
    allowed_user_id: i64,
}

impl TelegramBot {
    pub fn new(token: String, allowed_user_id: i64) -> Self {
        let base_url = format!("https://api.telegram.org/bot{token}");
        Self {
            client: Client::new(),
            token,
            base_url,
            allowed_user_id,
        }
    }

    pub async fn get_me(&self) -> Result<User> {
        let url = format!("{}/getMe", self.base_url);

        let response = self
            .client
            .get(&url)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response
                .text()
                .await
                .unwrap_or_default();
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        let telegram_response: TelegramResponse<User> = response
            .json()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        if !telegram_response.ok {
            return Err(OasisError::Telegram(
                telegram_response
                    .description
                    .unwrap_or_else(|| "unknown error".to_string()),
            ));
        }

        telegram_response
            .result
            .ok_or_else(|| OasisError::Telegram("missing result in response".to_string()))
    }

    pub async fn get_updates(&self, offset: i64, timeout: u32) -> Result<Vec<Update>> {
        let url = format!("{}/getUpdates", self.base_url);

        let body = serde_json::json!({
            "offset": offset,
            "timeout": timeout,
            "allowed_updates": ["message"],
        });

        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response
                .text()
                .await
                .unwrap_or_default();
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        let telegram_response: TelegramResponse<Vec<Update>> = response
            .json()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        if !telegram_response.ok {
            return Err(OasisError::Telegram(
                telegram_response
                    .description
                    .unwrap_or_else(|| "unknown error".to_string()),
            ));
        }

        telegram_response
            .result
            .ok_or_else(|| OasisError::Telegram("missing result in response".to_string()))
    }

    pub async fn send_message(&self, chat_id: i64, text: &str) -> Result<()> {
        let chunks = split_message(text);

        for chunk in chunks {
            self.send_single_message(chat_id, &chunk).await?;
        }

        Ok(())
    }

    async fn send_single_message(&self, chat_id: i64, text: &str) -> Result<()> {
        let url = format!("{}/sendMessage", self.base_url);

        let html = markdown_to_html(text);
        let body = serde_json::json!({
            "chat_id": chat_id,
            "text": html,
            "parse_mode": "HTML",
        });

        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response
                .text()
                .await
                .unwrap_or_default();
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        let telegram_response: TelegramResponse<serde_json::Value> = response
            .json()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        if !telegram_response.ok {
            return Err(OasisError::Telegram(
                telegram_response
                    .description
                    .unwrap_or_else(|| "unknown error".to_string()),
            ));
        }

        Ok(())
    }

    /// Send a message and return the message_id (for later editing).
    pub async fn send_message_with_id(&self, chat_id: i64, text: &str) -> Result<i64> {
        let url = format!("{}/sendMessage", self.base_url);

        let body = serde_json::json!({
            "chat_id": chat_id,
            "text": text,
        });

        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response.text().await.unwrap_or_default();
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        let telegram_response: TelegramResponse<serde_json::Value> = response
            .json()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        if !telegram_response.ok {
            return Err(OasisError::Telegram(
                telegram_response
                    .description
                    .unwrap_or_else(|| "unknown error".to_string()),
            ));
        }

        let msg_id = telegram_response
            .result
            .and_then(|r| r["message_id"].as_i64())
            .ok_or_else(|| OasisError::Telegram("missing message_id in response".to_string()))?;

        Ok(msg_id)
    }

    /// Edit a message as plain text (no formatting). Use for streaming intermediate edits
    /// where markdown is likely incomplete.
    pub async fn edit_message(&self, chat_id: i64, message_id: i64, text: &str) -> Result<()> {
        let url = format!("{}/editMessageText", self.base_url);

        let body = serde_json::json!({
            "chat_id": chat_id,
            "message_id": message_id,
            "text": text,
        });

        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response.text().await.unwrap_or_default();
            if body.contains("message is not modified") {
                return Ok(());
            }
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        Ok(())
    }

    /// Edit a message with formatting. Converts Markdown to Telegram HTML.
    /// Falls back to plain text if Telegram rejects the HTML.
    pub async fn edit_message_formatted(&self, chat_id: i64, message_id: i64, text: &str) -> Result<()> {
        let url = format!("{}/editMessageText", self.base_url);

        let html = markdown_to_html(text);
        let body = serde_json::json!({
            "chat_id": chat_id,
            "message_id": message_id,
            "text": html,
            "parse_mode": "HTML",
        });

        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if status.is_success() {
            return Ok(());
        }

        let resp_body = response.text().await.unwrap_or_default();
        if resp_body.contains("message is not modified") {
            return Ok(());
        }

        // HTML rejected — fall back to plain text
        self.edit_message(chat_id, message_id, text).await
    }

    pub async fn send_typing(&self, chat_id: i64) -> Result<()> {
        let url = format!("{}/sendChatAction", self.base_url);

        let body = serde_json::json!({
            "chat_id": chat_id,
            "action": "typing",
        });

        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response
                .text()
                .await
                .unwrap_or_default();
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        let telegram_response: TelegramResponse<serde_json::Value> = response
            .json()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        if !telegram_response.ok {
            return Err(OasisError::Telegram(
                telegram_response
                    .description
                    .unwrap_or_else(|| "unknown error".to_string()),
            ));
        }

        Ok(())
    }

    pub async fn get_file(&self, file_id: &str) -> Result<File> {
        let url = format!("{}/getFile", self.base_url);

        let body = serde_json::json!({
            "file_id": file_id,
        });

        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response
                .text()
                .await
                .unwrap_or_default();
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        let telegram_response: TelegramResponse<File> = response
            .json()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        if !telegram_response.ok {
            return Err(OasisError::Telegram(
                telegram_response
                    .description
                    .unwrap_or_else(|| "unknown error".to_string()),
            ));
        }

        telegram_response
            .result
            .ok_or_else(|| OasisError::Telegram("missing result in response".to_string()))
    }

    pub async fn download_file(&self, file_path: &str) -> Result<Vec<u8>> {
        let url = format!(
            "https://api.telegram.org/file/bot{}/{}",
            self.token, file_path
        );

        let response = self
            .client
            .get(&url)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response
                .text()
                .await
                .unwrap_or_default();
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        response
            .bytes()
            .await
            .map(|b| b.to_vec())
            .map_err(|e| OasisError::Telegram(e.to_string()))
    }

    pub fn is_authorized(&self, user_id: i64) -> bool {
        user_id == self.allowed_user_id
    }

    /// Register bot commands with Telegram so they appear in the command menu.
    pub async fn set_my_commands(&self, commands: &[(&str, &str)]) -> Result<()> {
        let url = format!("{}/setMyCommands", self.base_url);

        let cmds: Vec<serde_json::Value> = commands
            .iter()
            .map(|(cmd, desc)| serde_json::json!({ "command": cmd, "description": desc }))
            .collect();

        let body = serde_json::json!({ "commands": cmds });

        let response = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .map_err(|e| OasisError::Telegram(e.to_string()))?;

        let status = response.status();
        if !status.is_success() {
            let body = response.text().await.unwrap_or_default();
            return Err(OasisError::Http {
                status: status.as_u16(),
                body,
            });
        }

        Ok(())
    }
}

/// Convert standard Markdown to Telegram-compatible HTML using pulldown-cmark.
///
/// Telegram supports: <b>, <i>, <u>, <s>, <code>, <pre>, <a href="">, <blockquote>.
/// Headers are rendered as bold text. Unsupported elements are passed through as text.
fn markdown_to_html(text: &str) -> String {
    use pulldown_cmark::{Event, Options, Parser, Tag, TagEnd, CodeBlockKind};

    let options = Options::ENABLE_STRIKETHROUGH;
    let parser = Parser::new_ext(text, options);

    let mut html = String::with_capacity(text.len() + 128);
    let mut in_code_block = false;

    for event in parser {
        match event {
            Event::Start(tag) => match tag {
                Tag::Heading { .. } => html.push_str("\n<b>"),
                Tag::Paragraph => {}
                Tag::Strong => html.push_str("<b>"),
                Tag::Emphasis => html.push_str("<i>"),
                Tag::Strikethrough => html.push_str("<s>"),
                Tag::BlockQuote(_) => html.push_str("<blockquote>"),
                Tag::CodeBlock(kind) => {
                    in_code_block = true;
                    match kind {
                        CodeBlockKind::Fenced(lang) if !lang.is_empty() => {
                            html.push_str(&format!("<pre><code class=\"language-{}\">", html_escape(&lang)));
                        }
                        _ => html.push_str("<pre><code>"),
                    }
                }
                Tag::Link { dest_url, .. } => {
                    html.push_str(&format!("<a href=\"{}\">", html_escape(&dest_url)));
                }
                Tag::List(Some(start)) => {
                    // Numbered list — we track the number via Item events
                    html.push_str(&format!("\n{start}. "));
                }
                Tag::List(None) => html.push('\n'),
                Tag::Item => html.push_str("• "),
                _ => {}
            },
            Event::End(tag) => match tag {
                TagEnd::Heading(_) => html.push_str("</b>\n"),
                TagEnd::Paragraph => html.push('\n'),
                TagEnd::Strong => html.push_str("</b>"),
                TagEnd::Emphasis => html.push_str("</i>"),
                TagEnd::Strikethrough => html.push_str("</s>"),
                TagEnd::BlockQuote(_) => html.push_str("</blockquote>"),
                TagEnd::CodeBlock => {
                    in_code_block = false;
                    html.push_str("</code></pre>");
                }
                TagEnd::Link => html.push_str("</a>"),
                TagEnd::Item => html.push('\n'),
                TagEnd::List(_) => {}
                _ => {}
            },
            Event::Text(text) => {
                if in_code_block {
                    html.push_str(&html_escape(&text));
                } else {
                    html.push_str(&html_escape(&text));
                }
            }
            Event::Code(code) => {
                html.push_str("<code>");
                html.push_str(&html_escape(&code));
                html.push_str("</code>");
            }
            Event::SoftBreak => html.push('\n'),
            Event::HardBreak => html.push('\n'),
            Event::Rule => html.push_str("\n---\n"),
            _ => {}
        }
    }

    // Trim trailing whitespace
    let trimmed = html.trim();
    trimmed.to_string()
}

fn html_escape(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

fn split_message(text: &str) -> Vec<String> {
    if text.len() <= MAX_MESSAGE_LENGTH {
        return vec![text.to_string()];
    }

    let mut chunks = Vec::new();
    let mut remaining = text;

    while !remaining.is_empty() {
        if remaining.len() <= MAX_MESSAGE_LENGTH {
            chunks.push(remaining.to_string());
            break;
        }

        let split_at = &remaining[..MAX_MESSAGE_LENGTH];

        let split_pos = match split_at.rfind('\n') {
            Some(pos) => pos + 1,
            None => MAX_MESSAGE_LENGTH,
        };

        chunks.push(remaining[..split_pos].to_string());
        remaining = &remaining[split_pos..];
    }

    chunks
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_markdown_bold() {
        let result = markdown_to_html("This is **bold** text");
        assert!(result.contains("<b>bold</b>"));
    }

    #[test]
    fn test_markdown_italic() {
        let result = markdown_to_html("This is *italic* text");
        assert!(result.contains("<i>italic</i>"));
    }

    #[test]
    fn test_markdown_code_inline() {
        let result = markdown_to_html("Use `println!` here");
        assert!(result.contains("<code>println!</code>"));
    }

    #[test]
    fn test_markdown_code_block() {
        let result = markdown_to_html("```rust\nfn main() {}\n```");
        assert!(result.contains("<pre>"));
        assert!(result.contains("fn main()"));
        assert!(result.contains("</pre>"));
    }

    #[test]
    fn test_markdown_header() {
        let result = markdown_to_html("### Section Title");
        assert!(result.contains("<b>Section Title</b>"));
    }

    #[test]
    fn test_markdown_header_with_bold() {
        let result = markdown_to_html("### **Bold Header**");
        assert!(result.contains("<b>"));
        assert!(result.contains("Bold Header"));
    }

    #[test]
    fn test_markdown_link() {
        let result = markdown_to_html("[click here](https://example.com)");
        assert!(result.contains("<a href=\"https://example.com\">click here</a>"));
    }

    #[test]
    fn test_html_escape() {
        let result = markdown_to_html("1 < 2 & 3 > 0");
        assert!(result.contains("&lt;"));
        assert!(result.contains("&amp;"));
        assert!(result.contains("&gt;"));
    }

    #[test]
    fn test_markdown_mixed() {
        let input = "### Konsep Utama\n**Loss Aversion**: Manusia *takut* kehilangan.";
        let result = markdown_to_html(input);
        assert!(result.contains("<b>Konsep Utama</b>"));
        assert!(result.contains("<b>Loss Aversion</b>"));
        assert!(result.contains("<i>takut</i>"));
    }
}
