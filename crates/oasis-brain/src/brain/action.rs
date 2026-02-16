use oasis_core::error::Result;
use oasis_core::types::*;

use super::Brain;

impl Brain {
    /// Handle an action intent: run the generic tool execution loop.
    pub(crate) async fn handle_action(
        &self,
        chat_id: i64,
        text: &str,
        conversation_id: &str,
    ) -> Result<String> {
        let tools = self.tools.definitions();
        let task_summary = self
            .tasks
            .get_active_task_summary(self.config.brain.timezone_offset)
            .await?;

        let memory_context = match self.memory.build_memory_context().await {
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

        let mut messages = self.build_system_prompt(&task_summary, &memory_context, &recent);

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
                 Once browsing, keep using browser tools — do not switch to web_search.\n",
            );
        }

        messages.push(ChatMessage::text("user", text));

        // Send placeholder
        let msg_id = self.bot.send_message_with_id(chat_id, "...").await?;

        const MAX_ITERATIONS: usize = 10;
        let mut final_text = String::new();

        for iteration in 0..MAX_ITERATIONS {
            log!(" [action] iteration {}/{MAX_ITERATIONS}", iteration + 1);

            let request = ChatRequest {
                messages: messages.clone(),
                max_tokens: Some(4096),
                temperature: Some(0.7),
            };

            let response = match self.llm.chat_with_tools(request, &tools).await {
                Ok(r) => r,
                Err(e) => {
                    log!(" [action] LLM error: {e}");
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

            // Execute each tool call via the registry
            let mut last_output = String::new();
            for tool_call in &response.tool_calls {
                log!(" [tool] {}({})", tool_call.name, tool_call.arguments);
                let _ = self
                    .bot
                    .edit_message(
                        chat_id,
                        msg_id,
                        &format!("Using {}...", tool_call.name),
                    )
                    .await;

                let result = self
                    .tools
                    .execute(&tool_call.name, &tool_call.arguments)
                    .await;
                log!(" [tool] {} → {} chars", tool_call.name, result.output.len());
                last_output = result.output.clone();

                messages.push(ChatMessage::tool_result(&tool_call.id, &result.output));
            }

            // Short-circuit for single simple tools
            if response.tool_calls.len() == 1 && !last_output.starts_with("Error:") {
                let is_simple = matches!(
                    response.tool_calls[0].name.as_str(),
                    "task_create"
                        | "task_list"
                        | "task_update"
                        | "task_delete"
                        | "remember"
                        | "schedule_create"
                        | "schedule_list"
                        | "schedule_update"
                        | "schedule_delete"
                );
                if is_simple {
                    log!(" [action] short-circuit: simple tool, skipping LLM synthesis");
                    final_text = last_output;
                    break;
                }
            }
        }

        // If we exhausted iterations without a final text, force one
        if final_text.is_empty() {
            log!(" [action] forcing final response (max iterations reached)");
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
                    log!(" [action] final LLM error: {e}");
                    final_text = "Sorry, something went wrong. Please try again.".to_string();
                }
            }
        }

        if final_text.is_empty() {
            final_text = "Done.".to_string();
        }

        let _ = self
            .bot
            .edit_message_formatted(chat_id, msg_id, &final_text)
            .await;
        log!(" [send] {} chars (action)", final_text.len());

        // Close any active browser session to free resources
        self.search_tool.close_browse_session().await;

        Ok(final_text)
    }
}
