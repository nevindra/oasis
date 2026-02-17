use std::sync::Arc;

use oasis_core::error::Result;
use oasis_core::types::*;

use super::Brain;
use crate::service::memory::MemoryStore;

impl Brain {
    /// Spawn a background task to embed + store a message pair, then extract user facts.
    pub(crate) fn spawn_store(
        self: &Arc<Self>,
        conv_id: String,
        user_text: String,
        assistant_text: String,
    ) {
        let brain = Arc::clone(self);
        tokio::spawn(async move {
            if let Err(e) = brain
                .store_message_pair(&conv_id, &user_text, &assistant_text)
                .await
            {
                log!(" [store] background failed: {e}");
            }

            brain
                .extract_and_store_facts(&user_text, &assistant_text)
                .await;
        });
    }

    /// Extract user facts from a conversation turn and store them.
    async fn extract_and_store_facts(&self, user_text: &str, assistant_text: &str) {
        // Skip trivial messages
        if !crate::service::memory::should_extract_facts(user_text) {
            return;
        }

        let existing = match self.memory.get_top_facts(30).await {
            Ok(facts) => facts,
            Err(_) => Vec::new(),
        };

        let mut prompt = crate::service::memory::EXTRACT_FACTS_PROMPT.to_string();
        if !existing.is_empty() {
            prompt.push_str("\n\n## Already known facts (do NOT re-extract these)\n");
            for f in &existing {
                prompt.push_str(&format!("- {}\n", f.fact));
            }
            prompt.push_str("\nOnly extract NEW facts not already covered above.");
        }

        let conversation_turn = format!("User: {user_text}\nAssistant: {assistant_text}");

        let request = ChatRequest {
            messages: vec![
                ChatMessage::text("system", prompt),
                ChatMessage::text("user", conversation_turn),
            ],
            max_tokens: Some(512),
            temperature: Some(0.0),
        };

        let response = match self.llm.chat_intent(request).await {
            Ok(r) => r,
            Err(e) => {
                log!(" [memory] fact extraction failed: {e}");
                return;
            }
        };

        let facts = MemoryStore::parse_extracted_facts(&response.content);
        if facts.is_empty() {
            return;
        }
        log!(" [memory] extracted {} fact(s)", facts.len());

        // Embed all fact texts in one batch
        let fact_texts: Vec<&str> = facts.iter().map(|f| f.fact.as_str()).collect();
        let embeddings = match self.embedder.embed(&fact_texts).await {
            Ok(e) => e,
            Err(e) => {
                log!(" [memory] fact embedding failed: {e}");
                return;
            }
        };

        for (fact, embedding) in facts.iter().zip(embeddings.iter()) {
            // Handle contradiction: delete superseded fact first
            if let Some(ref old_fact) = fact.supersedes {
                if let Err(e) = self.memory.delete_matching_facts(old_fact).await {
                    log!(" [memory] failed to delete superseded fact: {e}");
                }
            }

            if let Err(e) = self
                .memory
                .upsert_fact(&fact.fact, &fact.category, embedding, None)
                .await
            {
                log!(" [memory] failed to upsert fact: {e}");
            }
        }
    }

    /// Store a user message and an assistant response in the conversation.
    async fn store_message_pair(
        &self,
        conversation_id: &str,
        user_text: &str,
        assistant_text: &str,
    ) -> Result<()> {
        let now = now_unix();

        log!(" [store] saving message pair to conversation {conversation_id}");
        let user_embedding = match self.embedder.embed(&[user_text]).await {
            Ok(embeddings) => embeddings.into_iter().next(),
            Err(e) => {
                log!(" [store] embed failed (non-fatal): {e}");
                None
            }
        };

        let user_msg = Message {
            id: new_id(),
            conversation_id: conversation_id.to_string(),
            role: "user".to_string(),
            content: user_text.to_string(),
            created_at: now,
        };
        self.store
            .insert_message(&user_msg, user_embedding.as_deref())
            .await?;

        let assistant_msg = Message {
            id: new_id(),
            conversation_id: conversation_id.to_string(),
            role: "assistant".to_string(),
            content: assistant_text.to_string(),
            created_at: now,
        };
        self.store.insert_message(&assistant_msg, None).await?;

        Ok(())
    }
}
