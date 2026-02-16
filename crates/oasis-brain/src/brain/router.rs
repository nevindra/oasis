use std::sync::Arc;

use oasis_core::error::Result;
use oasis_core::types::*;

use super::Brain;
use crate::service::intent::{parse_intent, Intent};

/// Intent classification prompt sent to the intent LLM.
const INTENT_SYSTEM_PROMPT: &str = r#"You are an intent classifier for a personal assistant. Classify the user message into exactly one of two intents.

Return a JSON object with a single "intent" field:

1. **chat** — Conversation, questions, opinions, recommendations, explanations, or anything the assistant can answer from its own knowledge. This includes: "what is X?", "recommend me Y", "what do you think about Z?", "tell me about...", follow-up questions, casual talk, greetings.
   Return: `{"intent":"chat"}`

2. **action** — The user explicitly wants to CREATE, UPDATE, DELETE, or SEARCH for something using a tool. Examples: "buatkan task ...", "tandai ... selesai", "hapus task ...", "cari di internet ...", "ingatkan aku ...", "cari di knowledge base".
   Return: `{"intent":"action"}`

## Rules
- If the user is asking a question or having a conversation, it's CHAT — even if the topic involves tasks, books, schedules, etc.
- Action is ONLY when the user wants to PERFORM an operation (create, update, delete, search, save).
- If in doubt, prefer CHAT.
- Respond with ONLY the JSON object, no extra text.
- The user may write in English or Indonesian."#;

impl Brain {
    /// The main run loop: long-poll Telegram for updates, handle each message.
    pub async fn run(self: &Arc<Self>) -> Result<()> {
        let me = self.bot.get_me().await?;
        log!(
            "bot started as @{}",
            me.username.as_deref().unwrap_or("unknown")
        );

        let _ = self
            .bot
            .set_my_commands(&[("new", "Start a new conversation")])
            .await;

        // Spawn the scheduled actions background loop
        {
            let brain = Arc::clone(self);
            tokio::spawn(async move {
                brain.run_scheduled_actions_loop().await;
            });
        }

        let mut offset: i64 = self
            .store
            .get_config("telegram_offset")
            .await?
            .and_then(|s| s.parse().ok())
            .unwrap_or(0);

        loop {
            let updates = match self.bot.get_updates(offset, 30).await {
                Ok(u) => u,
                Err(e) => {
                    log!(" error polling updates: {e}");
                    tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                    continue;
                }
            };

            for update in &updates {
                if update.update_id >= offset {
                    offset = update.update_id + 1;
                }

                if let Some(ref msg) = update.message {
                    if let Err(e) = self.handle_message(msg).await {
                        log!(" error handling message: {e}");
                    }
                }
            }

            if !updates.is_empty() {
                let _ = self
                    .store
                    .set_config("telegram_offset", &offset.to_string())
                    .await;
            }
        }
    }

    /// Route an incoming Telegram message to the appropriate handler.
    async fn handle_message(
        self: &Arc<Self>,
        msg: &oasis_telegram::types::TelegramMessage,
    ) -> Result<()> {
        let user_id = msg.from.as_ref().map(|u| u.id).unwrap_or(0);
        let username = msg
            .from
            .as_ref()
            .and_then(|u| u.username.as_deref())
            .unwrap_or("?");
        log!(
            " [recv] from={username} (id={user_id}) chat={}",
            msg.chat.id
        );

        if !self.is_owner(user_id).await? {
            log!(" [auth] DENIED user_id={user_id}");
            return Ok(());
        }
        log!(" [auth] OK");

        let chat_id = msg.chat.id;
        let _ = self.bot.send_typing(chat_id).await;

        let conversation = self.store.get_or_create_conversation(chat_id).await?;
        log!(" [conv] id={}", conversation.id);

        // Handle document uploads (structural — no intent classification needed)
        if let Some(ref doc) = msg.document {
            let fname = doc.file_name.as_deref().unwrap_or("?");
            log!(" [file] name={fname} id={}", doc.file_id);
            let response = self.handle_file(doc, &conversation.id).await?;
            log!(" [send] {} chars", response.len());
            self.bot.send_message(chat_id, &response).await?;
            let user_text = msg
                .caption
                .as_deref()
                .unwrap_or("[file upload]")
                .to_string();
            self.spawn_store(conversation.id.clone(), user_text, response);
            return Ok(());
        }

        let text = match msg.text.as_deref() {
            Some(t) if !t.is_empty() => t,
            _ => {
                log!(" [skip] empty message");
                return Ok(());
            }
        };
        log!(" [text] \"{}\"", &text[..text.len().min(80)]);

        // /new command — start a new conversation (silent)
        if text.trim() == "/new" {
            self.store.create_new_conversation(chat_id).await?;
            log!(" [cmd] /new — created new conversation");
            return Ok(());
        }

        // URL messages (structural — no intent classification needed)
        if text.starts_with("http://") || text.starts_with("https://") {
            log!(" [route] url");
            let response = self.handle_url(text, &conversation.id).await?;
            log!(" [send] {} chars", response.len());
            self.bot.send_message(chat_id, &response).await?;
            self.spawn_store(conversation.id.clone(), text.to_string(), response);
            return Ok(());
        }

        // Classify intent via LLM
        let intent = self.classify_intent(text).await?;
        log!(" [intent] {intent:?}");

        match intent {
            Intent::Chat => {
                log!(" [route] chat");
                let recent_messages = self
                    .store
                    .get_recent_messages(&conversation.id, self.config.brain.context_window)
                    .await?;
                log!(" [history] {} recent messages", recent_messages.len());
                let response = self
                    .handle_chat_stream(chat_id, text, &recent_messages, "")
                    .await?;
                self.spawn_store(conversation.id.clone(), text.to_string(), response);
            }
            Intent::Action => {
                log!(" [route] action");
                let response = self.handle_action(chat_id, text, &conversation.id).await?;
                self.spawn_store(conversation.id.clone(), text.to_string(), response);
            }
        }

        Ok(())
    }

    /// Check if a user is authorized.
    async fn is_owner(&self, user_id: i64) -> Result<bool> {
        if let Some(owner_str) = self.store.get_config("owner_user_id").await? {
            if let Ok(owner_id) = owner_str.parse::<i64>() {
                return Ok(owner_id == user_id);
            }
        }

        if self.config.telegram.allowed_user_id != 0 {
            return Ok(self.config.telegram.allowed_user_id == user_id);
        }

        self.store
            .set_config("owner_user_id", &user_id.to_string())
            .await?;
        log!(" registered owner user_id={user_id}");
        Ok(true)
    }

    /// Classify a user message into a structured Intent.
    async fn classify_intent(&self, message: &str) -> Result<Intent> {
        let tz_offset = self.config.brain.timezone_offset;
        let (now_str, tz_str) = crate::brain::chat::format_now_with_tz(tz_offset);

        let system = INTENT_SYSTEM_PROMPT
            .replace("{now}", &now_str)
            .replace("{tz}", &tz_str);

        let request = ChatRequest {
            messages: vec![
                ChatMessage::text("system", system),
                ChatMessage::text("user", message),
            ],
            max_tokens: Some(256),
            temperature: Some(0.0),
        };

        let response = self.llm.chat_intent(request).await?;
        let intent = parse_intent(&response.content);
        Ok(intent)
    }

    /// Handle a file upload from Telegram.
    async fn handle_file(
        &self,
        doc: &oasis_telegram::types::TelegramDocument,
        _conversation_id: &str,
    ) -> Result<String> {
        let file_info = self.bot.get_file(&doc.file_id).await?;
        let file_path = file_info.file_path.ok_or_else(|| {
            oasis_core::error::OasisError::Telegram(
                "file_path not returned by Telegram API".to_string(),
            )
        })?;
        let file_bytes = self.bot.download_file(&file_path).await?;
        let content = String::from_utf8(file_bytes).map_err(|e| {
            oasis_core::error::OasisError::Ingest(format!("file is not valid UTF-8 text: {e}"))
        })?;
        let filename = doc.file_name.as_deref().unwrap_or("unknown_file");
        self.memory_tool.ingest_file(&content, filename).await
    }

    /// Handle a URL message.
    async fn handle_url(&self, url: &str, _conversation_id: &str) -> Result<String> {
        let html = crate::service::ingest::pipeline::IngestPipeline::fetch_url(url).await?;
        self.memory_tool.ingest_url(&html, url).await
    }
}
