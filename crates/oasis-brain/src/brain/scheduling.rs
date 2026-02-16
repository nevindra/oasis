use std::sync::Arc;

use oasis_core::error::Result;
use oasis_core::types::*;

use super::Brain;
use crate::tool::schedule::compute_next_run;
use crate::tool::task::format_due;

impl Brain {
    /// Background loop: check for and execute due scheduled actions every 60s.
    pub(crate) async fn run_scheduled_actions_loop(self: &Arc<Self>) {
        log!(" [sched] scheduled actions loop started");

        loop {
            tokio::time::sleep(std::time::Duration::from_secs(60)).await;

            if let Err(e) = self.check_and_run_scheduled_actions().await {
                log!(" [sched] error: {e}");
            }
        }
    }

    async fn check_and_run_scheduled_actions(self: &Arc<Self>) -> Result<()> {
        let now = now_unix();
        let due_actions = self.store.get_due_scheduled_actions(now).await?;

        if due_actions.is_empty() {
            return Ok(());
        }

        let chat_id = match self.store.get_config("owner_user_id").await? {
            Some(id_str) => match id_str.parse::<i64>() {
                Ok(id) => id,
                Err(_) => return Ok(()),
            },
            None => return Ok(()),
        };

        for action in &due_actions {
            log!(" [sched] executing: {}", action.description);

            let tool_calls: Vec<ScheduledToolCall> = match serde_json::from_str(&action.tool_calls)
            {
                Ok(tc) => tc,
                Err(_) => {
                    match serde_json::from_str::<Vec<String>>(&action.tool_calls) {
                        Ok(strings) => {
                            let mut parsed = Vec::new();
                            let mut ok = true;
                            for s in &strings {
                                match serde_json::from_str::<ScheduledToolCall>(s) {
                                    Ok(tc) => parsed.push(tc),
                                    Err(e2) => {
                                        log!(" [sched] invalid tool_calls JSON: {e2}");
                                        ok = false;
                                        break;
                                    }
                                }
                            }
                            if !ok {
                                continue;
                            }
                            parsed
                        }
                        Err(e) => {
                            log!(" [sched] invalid tool_calls JSON: {e}");
                            continue;
                        }
                    }
                }
            };

            // Execute each tool via the registry
            let mut results = Vec::new();
            for tc in &tool_calls {
                log!(" [sched] tool: {}({})", tc.tool, tc.params);
                let result = self.tools.execute(&tc.tool, &tc.params).await;
                results.push(format!("## {}\n{}", tc.tool, result.output));
            }

            let combined = results.join("\n\n");

            let message = if let Some(ref prompt) = action.synthesis_prompt {
                self.synthesize_scheduled_result(&combined, prompt, &action.description)
                    .await
            } else {
                format!("**{}**\n\n{}", action.description, combined)
            };

            if let Err(e) = self.bot.send_message(chat_id, &message).await {
                log!(" [sched] send failed: {e}");
            }

            let tz = self.config.brain.timezone_offset;
            let next_run = compute_next_run(&action.schedule, now, tz).unwrap_or(now + 86400);

            if let Err(e) = self
                .store
                .update_scheduled_action_run(&action.id, now, next_run)
                .await
            {
                log!(" [sched] update run failed: {e}");
            }

            let next_str = format_due(next_run, tz);
            log!(" [sched] done: {}, next: {next_str}", action.description);
        }

        Ok(())
    }

    /// Synthesize scheduled action results using the intent LLM.
    async fn synthesize_scheduled_result(
        &self,
        tool_results: &str,
        synthesis_prompt: &str,
        description: &str,
    ) -> String {
        let tz_offset = self.config.brain.timezone_offset;
        let (now_str, tz_str) = crate::brain::chat::format_now_with_tz(tz_offset);

        let system = format!(
            "You are Oasis, a personal AI assistant. Current time: {now_str} (UTC{tz_str}).\n\n\
             You are generating a scheduled report: \"{description}\".\n\
             User's formatting instruction: {synthesis_prompt}\n\n\
             Based on the tool results below, create a concise, well-formatted message.\n\n\
             Tool results:\n{tool_results}"
        );

        let request = ChatRequest {
            messages: vec![
                ChatMessage::text("system", system),
                ChatMessage::text("user", "Generate the report."),
            ],
            max_tokens: Some(2048),
            temperature: Some(0.5),
        };

        match self.llm.chat_intent(request).await {
            Ok(resp) => resp.content,
            Err(e) => {
                log!(" [sched] synthesis failed: {e}");
                format!("**{description}**\n\n{tool_results}")
            }
        }
    }
}
