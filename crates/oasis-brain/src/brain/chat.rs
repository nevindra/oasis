use oasis_core::error::Result;
use oasis_core::types::*;

use super::Brain;
use crate::service::llm::is_transient_error;

const MAX_STREAM_RETRIES: u32 = 3;

impl Brain {
    /// Handle chat with streaming: send an initial placeholder message,
    /// then edit it as tokens arrive from the LLM.
    /// Retries up to MAX_STREAM_RETRIES times on transient errors (429, 5xx).
    pub(crate) async fn handle_chat_stream(
        &self,
        chat_id: i64,
        message: &str,
        recent_messages: &[Message],
        context: &str,
        images: Vec<oasis_core::types::ImageData>,
    ) -> Result<String> {
        let task_summary = self
            .tasks
            .get_active_task_summary(self.config.brain.timezone_offset)
            .await?;

        let memory_context = match self.memory.build_memory_context().await {
            Ok(mc) => mc,
            Err(e) => {
                log!(" [memory] failed to load memory context: {e}");
                String::new()
            }
        };
        let full_context = if memory_context.is_empty() {
            context.to_string()
        } else if context.is_empty() {
            memory_context
        } else {
            format!("{memory_context}\n{context}")
        };

        let messages = self.build_system_prompt(&task_summary, &full_context, recent_messages);

        let mut all_messages = messages;
        if images.is_empty() {
            all_messages.push(ChatMessage::text("user", message));
        } else {
            all_messages.push(ChatMessage::with_images(message, images));
        }

        let request = ChatRequest {
            messages: all_messages,
            max_tokens: Some(4096),
            temperature: Some(0.7),
        };

        let msg_id = self
            .bot
            .send_message_with_id(chat_id, "Thinking...")
            .await?;

        let mut last_err = None;

        for attempt in 0..=MAX_STREAM_RETRIES {
            if attempt > 0 {
                let delay = std::time::Duration::from_secs(1 << (attempt - 1));
                log!(
                    " [chat-llm] retry {attempt}/{MAX_STREAM_RETRIES} in {}s",
                    delay.as_secs()
                );
                let _ = self
                    .bot
                    .edit_message(
                        chat_id,
                        msg_id,
                        &format!("Retrying... (attempt {}/{})", attempt + 1, MAX_STREAM_RETRIES + 1),
                    )
                    .await;
                tokio::time::sleep(delay).await;
            }

            let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel::<String>();

            let llm_handle = {
                let llm = self.llm.clone();
                let req = request.clone();
                tokio::spawn(async move { llm.chat_stream(req, tx).await })
            };

            let mut accumulated = String::new();
            let mut last_edit = std::time::Instant::now();
            let edit_interval = std::time::Duration::from_secs(1);

            while let Some(chunk) = rx.recv().await {
                accumulated.push_str(&chunk);

                if last_edit.elapsed() >= edit_interval {
                    let _ = self.bot.edit_message(chat_id, msg_id, &accumulated).await;
                    last_edit = std::time::Instant::now();
                }
            }

            if !accumulated.is_empty() {
                let _ = self
                    .bot
                    .edit_message_formatted(chat_id, msg_id, &accumulated)
                    .await;
            }
            log!(" [send] {} chars (streamed)", accumulated.len());

            match llm_handle.await {
                Ok(Ok(resp)) => {
                    if let Some(ref u) = resp.usage {
                        log!(
                            " [llm] tokens: in={} out={}",
                            u.input_tokens,
                            u.output_tokens
                        );
                    }
                    if accumulated.is_empty() {
                        log!(" [llm] stream returned empty content");
                        let _ = self
                            .bot
                            .edit_message(
                                chat_id,
                                msg_id,
                                "Sorry, I got an empty response. Please try again.",
                            )
                            .await;
                    }
                    return Ok(accumulated);
                }
                Ok(Err(e)) => {
                    log!(" [llm] stream error: {e}");
                    if accumulated.is_empty()
                        && is_transient_error(&e)
                        && attempt < MAX_STREAM_RETRIES
                    {
                        last_err = Some(e);
                        continue;
                    }
                    if accumulated.is_empty() {
                        let _ = self
                            .bot
                            .edit_message(
                                chat_id,
                                msg_id,
                                "Sorry, something went wrong. Please try again.",
                            )
                            .await;
                        return Err(e);
                    }
                    return Ok(accumulated);
                }
                Err(e) => {
                    log!(" [llm] stream task panicked: {e}");
                    if accumulated.is_empty() {
                        let _ = self
                            .bot
                            .edit_message(
                                chat_id,
                                msg_id,
                                "Sorry, something went wrong. Please try again.",
                            )
                            .await;
                    }
                    return Ok(accumulated);
                }
            }
        }

        // All retries exhausted
        let _ = self
            .bot
            .edit_message(
                chat_id,
                msg_id,
                "Sorry, the service is temporarily unavailable. Please try again later.",
            )
            .await;
        Err(last_err.unwrap_or_else(|| oasis_core::error::OasisError::Llm {
            provider: self.config.llm.provider.clone(),
            message: "max retries exceeded".to_string(),
        }))
    }

    /// Build the dynamic system prompt with task summary, knowledge context,
    /// and recent conversation history.
    pub(crate) fn build_system_prompt(
        &self,
        task_summary: &str,
        context: &str,
        recent: &[Message],
    ) -> Vec<ChatMessage> {
        let today = {
            let tz_offset = self.config.brain.timezone_offset;
            let (now_str, tz_str) = format_now_with_tz(tz_offset);
            format!("{now_str} (UTC{tz_str})")
        };

        let mut system = format!(
            "You are Oasis, a personal AI assistant. You are helpful, concise, and friendly.\n\
             Current date and time: {today}\n\n\
             ## Active tasks\n\
             {task_summary}\n"
        );

        if !context.is_empty() {
            system.push_str(&format!("\n{context}\n"));
        }

        if !recent.is_empty() {
            system.push_str(
                "\n## Recent conversation (for context only — respond to the user's NEW message, not these)\n",
            );
            for msg in recent {
                let role_label = match msg.role.as_str() {
                    "user" => "User",
                    "assistant" => "Oasis",
                    _ => &msg.role,
                };
                system.push_str(&format!("{role_label}: {}\n", msg.content));
            }
        }

        vec![ChatMessage::text("system", system)]
    }
}


/// Format the current date+time in the user's timezone.
pub(crate) fn format_now_with_tz(tz_offset: i32) -> (String, String) {
    let utc_secs = now_unix();
    let local_secs = utc_secs + (tz_offset as i64) * 3600;
    let days = local_secs / 86400;
    let remainder = local_secs % 86400;
    let (y, m, d) = crate::tool::task::unix_days_to_date(days);
    let h = remainder / 3600;
    let min = (remainder % 3600) / 60;

    let datetime = format!("{y:04}-{m:02}-{d:02}T{h:02}:{min:02}");
    let tz_label = if tz_offset >= 0 {
        format!("+{:02}:00", tz_offset)
    } else {
        format!("-{:02}:00", tz_offset.unsigned_abs())
    };

    (datetime, tz_label)
}
