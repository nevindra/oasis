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

2. **action** — The user wants to CREATE, UPDATE, DELETE, SEARCH, SCHEDULE, or MONITOR something using a tool. This includes:
   - Search: "cari di internet ...", "cari di knowledge base"
   - Reminders and scheduling: "ingatkan aku ...", "cek lagi nanti ...", "tolong pantau ...", "kabari kalau ...", "remind me ...", "check later ..."
   - Any request that implies a deferred or future action the assistant should perform
   Return: `{"intent":"action"}`

## Rules
- If the user is asking a question or having a conversation, it's CHAT — even if the topic involves books, schedules, etc.
- Action is when the user wants to PERFORM an operation (create, update, delete, search, save, schedule, monitor).
- Requests to do something later or check on something in the future are ACTION, not CHAT.
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
            .set_my_commands(&[
                ("new", "Start a new conversation"),
                ("status", "Show active agents"),
            ])
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

                if let Some(msg) = update.message.clone() {
                    let brain = Arc::clone(self);
                    tokio::spawn(async move {
                        if let Err(e) = brain.handle_message(&msg).await {
                            log!(" error handling message: {e}");
                        }
                    });
                }
            }

            if !updates.is_empty() {
                let _ = self
                    .store
                    .set_config("telegram_offset", &offset.to_string())
                    .await;
            }

            // Try to dequeue queued actions when slots are available
            while let Some(queued) = self.agent_manager.try_dequeue() {
                log!(" [agent] dequeuing action from queue");
                if let Err(e) = self
                    .launch_agent(
                        queued.chat_id,
                        queued.text,
                        queued.conversation_id,
                        queued.original_message_id,
                    )
                    .await
                {
                    log!(" [agent] failed to launch dequeued action: {e}");
                }
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

        // --- Reply routing: check if this is a reply to an agent's ask_user question ---
        if let Some(ref reply_to) = msg.reply_to_message {
            if let Some(text) = msg.text.as_deref() {
                if self.agent_manager.route_reply(reply_to.message_id, text) {
                    log!(" [agent] routed reply to agent (reply_to={})", reply_to.message_id);
                    return Ok(());
                }
            }
        }

        let _ = self.bot.send_typing(chat_id).await;

        let conversation = self.store.get_or_create_conversation(chat_id).await?;
        log!(" [conv] id={}", conversation.id);

        // Handle document uploads (structural — no intent classification needed)
        if let Some(ref doc) = msg.document {
            let fname = doc.file_name.as_deref().unwrap_or("?");
            log!(" [file] name={fname} id={}", doc.file_id);
            let (ingest_response, extracted_text) = self.handle_file(doc).await?;
            log!(" [ingest] {ingest_response}");

            let caption = msg.caption.as_deref();

            // If user included a caption (instruction), use the file content as
            // context and answer via chat LLM. Otherwise just confirm ingestion.
            let response = if let Some(caption) = caption {
                log!(" [file] caption present, routing to chat with file context");
                // Truncate context to avoid exceeding LLM limits (~30k chars ≈ 7.5k tokens)
                let max_context = 30_000;
                let context = if extracted_text.len() > max_context {
                    format!(
                        "## File: {fname}\n(truncated to first {max_context} chars)\n\n{}",
                        &extracted_text[..max_context]
                    )
                } else {
                    format!("## File: {fname}\n\n{extracted_text}")
                };
                let recent_messages = self
                    .store
                    .get_recent_messages(&conversation.id, self.config.brain.context_window)
                    .await?;
                self.handle_chat_stream(chat_id, caption, &recent_messages, &context, vec![])
                    .await?
            } else {
                self.bot.send_message(chat_id, &ingest_response).await?;
                ingest_response
            };

            let user_text = caption.unwrap_or("[file upload]").to_string();
            self.spawn_store(conversation.id.clone(), user_text, response);
            return Ok(());
        }

        // Handle photo uploads (vision — send to LLM for understanding)
        if let Some(ref photos) = msg.photo {
            if !photos.is_empty() {
                log!(" [photo] {} size(s) available", photos.len());
                let response = self
                    .handle_photo(chat_id, photos, msg.caption.as_deref(), &conversation.id)
                    .await?;
                let user_text = msg
                    .caption
                    .as_deref()
                    .unwrap_or("[photo]")
                    .to_string();
                self.spawn_store(conversation.id.clone(), user_text, response);
                return Ok(());
            }
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

        // /status command — show active agents
        if text.trim() == "/status" {
            self.handle_status_command(chat_id).await?;
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
                    .handle_chat_stream(chat_id, text, &recent_messages, "", vec![])
                    .await?;
                self.spawn_store(conversation.id.clone(), text.to_string(), response);
            }
            Intent::Action => {
                log!(" [route] action (sub-agent)");
                self.spawn_action_agent(
                    chat_id,
                    text,
                    &conversation.id,
                    msg.message_id,
                )
                .await?;
            }
        }

        Ok(())
    }

    /// Handle `/status` command — show active agents.
    async fn handle_status_command(&self, chat_id: i64) -> Result<()> {
        let active = self.agent_manager.list_active();
        if active.is_empty() {
            self.bot.send_message(chat_id, "No active agents.").await?;
        } else {
            let mut msg = String::from("Active agents:\n");
            for (id, desc, status, elapsed) in &active {
                let short_id = &id[..id.len().min(8)];
                msg.push_str(&format!(
                    "- [{short_id}] {desc} ({status}, {elapsed}s)\n"
                ));
            }
            self.bot.send_message(chat_id, &msg).await?;
        }
        log!(" [cmd] /status — {} active", active.len());
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
        let (now_str, tz_str) = crate::util::format_now_with_tz(tz_offset);

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
}
