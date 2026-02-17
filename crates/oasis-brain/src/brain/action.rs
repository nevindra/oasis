use oasis_core::error::Result;
use oasis_core::types::*;

use super::Brain;
use crate::agent::AgentStatus;

/// Tool definition for ask_user, injected alongside registry tools.
fn ask_user_definition() -> ToolDefinition {
    ToolDefinition {
        name: "ask_user".to_string(),
        description: "Ask the user a clarifying question when you need more information to proceed.".to_string(),
        parameters: serde_json::json!({
            "type": "object",
            "properties": {
                "question": {
                    "type": "string",
                    "description": "The question to ask the user"
                }
            },
            "required": ["question"]
        }),
    }
}

impl Brain {
    /// Handle an action intent as a sub-agent with ask_user support.
    pub(crate) async fn handle_action(
        &self,
        chat_id: i64,
        text: &str,
        conversation_id: &str,
        agent_id: &str,
        ack_message_id: i64,
        original_message_id: i64,
        input_rx: &mut tokio::sync::mpsc::UnboundedReceiver<String>,
    ) -> Result<String> {
        // Resolve skill for this request
        let resolved = match self.skill_manager.resolve_skill(text).await {
            Ok(r) => r,
            Err(e) => {
                log!(" [agent:{agent_id}] skill resolution failed: {e}");
                None
            }
        };

        let mut tools = if let Some(ref resolved) = resolved {
            if let Some(ref allowed) = resolved.allowed_tools {
                // Filter to only the skill's allowed tools
                self.tools
                    .definitions()
                    .into_iter()
                    .filter(|d| allowed.contains(&d.name))
                    .collect()
            } else {
                self.tools.definitions()
            }
        } else {
            self.tools.definitions()
        };
        tools.push(ask_user_definition());

        let user_embedding = match self.embedder.embed(&[text]).await {
            Ok(mut e) => e.pop(),
            Err(e) => {
                log!(" [memory] embed for context failed: {e}");
                None
            }
        };
        let memory_context = match self
            .memory
            .build_memory_context(user_embedding.as_deref())
            .await
        {
            Ok(mc) => mc,
            Err(e) => {
                log!(" [memory] failed to load: {e}");
                String::new()
            }
        };

        let recent = self
            .store
            .get_recent_messages(conversation_id, self.config.brain.context_window)
            .await?;

        let mut messages = self.build_system_prompt(&memory_context, &recent);

        // Add tool usage guidelines to the system prompt for the action path
        if let Some(ChatMessage { content, .. }) = messages.first_mut() {
            content.push_str(
                "\n## Tool usage guidelines\n\
                 - **web_search**: Use for general information lookup, quick answers, and finding URLs.\n\
                 - **Browser tools** (browse_url, page_type, page_click, page_read): Use when the user asks to interact with a specific website. \
                 If the user mentions a site name, use browse_url to go there directly.\n\
                 - **CRITICAL — When to stop browsing**: If the page content already shows a list of results with names and prices, \
                 you MUST stop calling tools immediately and summarize that data. Do NOT click into individual items. \
                 The search results page IS the answer — extract the data from it.\n\
                 - **Browser tips**: Use short keywords (1-2 words) when typing into search fields. \
                 If a site shows autocomplete suggestions, click the best match before clicking search. \
                 If \"no results\", try a shorter keyword. browse_url already returns page content, no need for page_read after it. \
                 Once browsing, keep using browser tools — do not switch to web_search.\n\
                 - **ask_user**: Use when you need clarification from the user before proceeding. \
                 The user will be notified and can reply. Only use when truly needed.\n",
            );
        }

        // Inject skill instructions into system prompt if a skill was resolved
        if let Some(ref resolved) = resolved {
            if let Some(ChatMessage { content, .. }) = messages.first_mut() {
                content.push_str(&format!(
                    "\n## Active Skill: {}\n{}\n",
                    resolved.skill.name, resolved.skill.instructions
                ));
            }
            log!(" [agent:{agent_id}] skill injected: {}", resolved.skill.name);
        }

        messages.push(ChatMessage::text("user", text));

        const MAX_ITERATIONS: usize = 10;
        let mut final_text = String::new();

        for iteration in 0..MAX_ITERATIONS {
            log!(" [agent:{agent_id}] iteration {}/{MAX_ITERATIONS}", iteration + 1);

            let request = ChatRequest {
                messages: messages.clone(),
                max_tokens: Some(4096),
                temperature: Some(0.7),
            };

            let response = match self.llm.chat_with_tools(request, &tools).await {
                Ok(r) => r,
                Err(e) => {
                    log!(" [agent:{agent_id}] LLM error: {e}");
                    final_text = "Sorry, something went wrong. Please try again.".to_string();
                    break;
                }
            };

            if response.tool_calls.is_empty() {
                final_text = response.content;
                break;
            }

            // Add assistant's message (may contain both text and tool calls)
            let mut assistant_msg =
                ChatMessage::assistant_tool_calls(response.tool_calls.clone());
            if !response.content.is_empty() {
                assistant_msg.content = response.content.clone();
            }
            messages.push(assistant_msg);

            // Execute each tool call
            let mut last_output = String::new();
            for tool_call in &response.tool_calls {
                if tool_call.name == "ask_user" {
                    // --- ask_user: send question, wait for reply ---
                    let question = tool_call
                        .arguments
                        .get("question")
                        .and_then(|v| v.as_str())
                        .unwrap_or("Could you clarify?");

                    log!(" [agent:{agent_id}] ask_user: {question}");

                    let bot_msg_id = match self
                        .bot
                        .send_reply_with_id(chat_id, question, original_message_id)
                        .await
                    {
                        Ok(id) => id,
                        Err(e) => {
                            log!(" [agent:{agent_id}] failed to send question: {e}");
                            let output = "Error: failed to send question to user.";
                            messages.push(ChatMessage::tool_result(&tool_call.id, output));
                            last_output = output.to_string();
                            continue;
                        }
                    };

                    // Register for reply routing
                    self.agent_manager.register_message(bot_msg_id, agent_id.to_string());
                    self.agent_manager.set_status(agent_id, AgentStatus::WaitingForInput);

                    // Wait for user reply with 5-minute timeout
                    let answer = tokio::time::timeout(
                        std::time::Duration::from_secs(300),
                        input_rx.recv(),
                    )
                    .await;

                    self.agent_manager.set_status(agent_id, AgentStatus::Running);

                    let output = match answer {
                        Ok(Some(text)) => {
                            log!(" [agent:{agent_id}] got user reply: {}", &text[..text.len().min(80)]);
                            format!("User replied: {text}")
                        }
                        Ok(None) => {
                            log!(" [agent:{agent_id}] input channel closed");
                            "User did not respond. Proceed with your best judgment.".to_string()
                        }
                        Err(_) => {
                            log!(" [agent:{agent_id}] ask_user timed out");
                            "User did not respond within 5 minutes. Proceed with your best judgment.".to_string()
                        }
                    };

                    last_output = output.clone();
                    messages.push(ChatMessage::tool_result(&tool_call.id, &output));
                } else {
                    // --- Regular tool execution ---
                    log!(" [tool] {}({})", tool_call.name, tool_call.arguments);

                    let result = self
                        .tools
                        .execute(&tool_call.name, &tool_call.arguments)
                        .await;
                    log!(" [tool] {} -> {} chars", tool_call.name, result.output.len());
                    last_output = result.output.clone();

                    messages.push(ChatMessage::tool_result(&tool_call.id, &result.output));
                }
            }

            // Short-circuit for single simple tools
            if response.tool_calls.len() == 1
                && !last_output.starts_with("Error:")
                && response.tool_calls[0].name != "ask_user"
            {
                let is_simple = matches!(
                    response.tool_calls[0].name.as_str(),
                    "remember"
                        | "schedule_create"
                        | "schedule_list"
                        | "schedule_update"
                        | "schedule_delete"
                        | "skill_create"
                        | "skill_list"
                        | "skill_update"
                        | "skill_delete"
                );
                if is_simple {
                    log!(" [agent:{agent_id}] short-circuit: simple tool, skipping LLM synthesis");
                    final_text = last_output;
                    break;
                }
            }
        }

        // If we exhausted iterations without a final text, force one
        if final_text.is_empty() {
            log!(" [agent:{agent_id}] forcing final response (max iterations reached)");
            messages.push(ChatMessage::text(
                "user",
                "You have used all available tool calls. Now summarize what you found and respond to the user. \
                 If you found useful information, present it clearly. If you could not complete the task, explain what happened."
            ));
            let request = ChatRequest {
                messages: messages.clone(),
                max_tokens: Some(4096),
                temperature: Some(0.7),
            };
            match self.llm.chat_with_tools(request, &[]).await {
                Ok(r) => final_text = r.content,
                Err(e) => {
                    log!(" [agent:{agent_id}] final LLM error: {e}");
                    final_text = "Sorry, something went wrong. Please try again.".to_string();
                }
            }
        }

        if final_text.is_empty() {
            final_text = "Done.".to_string();
        }

        // Send result as a reply to the original message
        let _ = self
            .bot
            .send_reply_with_id(chat_id, &final_text, original_message_id)
            .await;

        // Update ack message to "Done"
        let _ = self
            .bot
            .edit_message(chat_id, ack_message_id, "Done.")
            .await;

        log!(" [agent:{agent_id}] sent {} chars (action)", final_text.len());

        // Close any active browser session to free resources
        self.search_tool.close_browse_session().await;

        Ok(final_text)
    }
}
