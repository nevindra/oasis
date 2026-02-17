use std::sync::Arc;

use oasis_core::error::Result;
use oasis_core::types::*;

use super::Brain;
use crate::agent::{AgentHandle, AgentStatus, QueuedAction};

impl Brain {
    /// Spawn an action as a background sub-agent, or enqueue if slots are full.
    pub(crate) async fn spawn_action_agent(
        self: &Arc<Self>,
        chat_id: i64,
        text: &str,
        conversation_id: &str,
        original_message_id: i64,
    ) -> Result<()> {
        if !self.agent_manager.slots_available() {
            log!(" [agent] slots full, enqueuing");
            self.agent_manager.enqueue(QueuedAction {
                chat_id,
                text: text.to_string(),
                conversation_id: conversation_id.to_string(),
                original_message_id,
            });
            let _ = self
                .bot
                .send_reply_with_id(chat_id, "Queued — will run when a slot opens.", original_message_id)
                .await;
            return Ok(());
        }

        self.launch_agent(chat_id, text.to_string(), conversation_id.to_string(), original_message_id)
            .await
    }

    /// Generate a brief ack message and a short task label from the user's request.
    /// Returns (ack_text, label). Uses the intent LLM (fast) in a single call.
    async fn generate_ack_and_label(&self, user_message: &str) -> (String, String) {
        let system = r#"You are a casual personal assistant. The user just asked you to do something (search, create a task, etc).

Return a JSON object with two fields:
- "ack": A brief, casual acknowledgment (1 sentence, max 20 words) in the SAME language as the user. Do NOT do the task — just acknowledge you'll work on it. No emojis.
- "label": A short task label (3-6 words, in English) summarizing what the agent will do. Examples: "Search CS:GO tournaments", "Create grocery task", "Find flight prices".

Respond with ONLY the JSON object, no extra text."#;

        let request = ChatRequest {
            messages: vec![
                ChatMessage::text("system", system),
                ChatMessage::text("user", user_message),
            ],
            max_tokens: Some(150),
            temperature: Some(0.7),
        };

        let fallback_label = user_message[..user_message.len().min(40)].to_string();

        match self.llm.chat_intent(request).await {
            Ok(r) => {
                // Try to parse JSON
                let content = r.content.trim().trim_start_matches("```json").trim_end_matches("```").trim();
                if let Ok(v) = serde_json::from_str::<serde_json::Value>(content) {
                    let ack = v["ack"].as_str().unwrap_or("On it...").to_string();
                    let label = v["label"].as_str().unwrap_or(&fallback_label).to_string();
                    (ack, label)
                } else {
                    // If not valid JSON, use the whole response as ack
                    (r.content, fallback_label)
                }
            }
            _ => ("On it...".to_string(), fallback_label),
        }
    }

    /// Launch a sub-agent task for an action.
    pub(crate) async fn launch_agent(
        self: &Arc<Self>,
        chat_id: i64,
        text: String,
        conversation_id: String,
        original_message_id: i64,
    ) -> Result<()> {
        let (ack_text, description) = self.generate_ack_and_label(&text).await;
        let ack_message_id = self
            .bot
            .send_reply_with_id(chat_id, &ack_text, original_message_id)
            .await?;

        let agent_id = new_id();
        let (input_tx, mut input_rx) = tokio::sync::mpsc::unbounded_channel::<String>();

        let handle = AgentHandle {
            id: agent_id.clone(),
            chat_id,
            status: std::sync::Mutex::new(AgentStatus::Running),
            description,
            input_tx,
            ack_message_id,
            original_message_id,
            created_at: now_unix() as u64,
        };
        self.agent_manager.register(handle);

        let brain = Arc::clone(self);
        let text = text.to_string();
        let conversation_id = conversation_id.to_string();
        let agent_id_clone = agent_id.clone();

        tokio::spawn(async move {
            log!(" [agent:{agent_id_clone}] started");

            let result = brain
                .handle_action(
                    chat_id,
                    &text,
                    &conversation_id,
                    &agent_id_clone,
                    ack_message_id,
                    original_message_id,
                    &mut input_rx,
                )
                .await;

            match result {
                Ok(response) => {
                    brain.spawn_store(conversation_id, text, response);
                }
                Err(e) => {
                    log!(" [agent:{agent_id_clone}] error: {e}");
                    let _ = brain
                        .bot
                        .send_reply_with_id(
                            chat_id,
                            "Sorry, something went wrong.",
                            original_message_id,
                        )
                        .await;
                }
            }

            // Cleanup
            brain.agent_manager.remove(&agent_id_clone);
            log!(" [agent:{agent_id_clone}] done, removed");
        });

        Ok(())
    }
}
