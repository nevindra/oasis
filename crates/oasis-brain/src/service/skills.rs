use std::sync::Arc;

use oasis_core::error::Result;
use oasis_core::types::*;

use crate::service::llm::{Embedder, LlmDispatch};
use crate::service::store::VectorStore;

/// Manages skill resolution: finding the best matching skill for a user request.
pub struct SkillManager {
    store: Arc<VectorStore>,
    embedder: Arc<Embedder>,
    llm: LlmDispatch,
}

/// The result of skill resolution: either a matched skill or no match.
pub struct ResolvedSkill {
    pub skill: Skill,
    /// Tool names this skill is allowed to use. None = all.
    pub allowed_tools: Option<Vec<String>>,
}

impl SkillManager {
    pub fn new(store: Arc<VectorStore>, embedder: Arc<Embedder>, llm: LlmDispatch) -> Self {
        Self { store, embedder, llm }
    }

    /// Two-stage skill resolution:
    /// 1. Semantic search on skill descriptions (top 3)
    /// 2. Intent LLM picks the best match or "none"
    pub async fn resolve_skill(&self, user_message: &str) -> Result<Option<ResolvedSkill>> {
        // Stage 1: semantic search
        let embeddings = self.embedder.embed(&[user_message]).await?;
        let embedding = match embeddings.into_iter().next() {
            Some(e) => e,
            None => return Ok(None),
        };
        let candidates = self.store.vector_search_skills(&embedding, 3).await?;

        if candidates.is_empty() {
            return Ok(None);
        }

        // Stage 2: intent LLM picks
        let mut options = String::new();
        for (i, skill) in candidates.iter().enumerate() {
            options.push_str(&format!("{}. {} — {}\n", i + 1, skill.name, skill.description));
        }

        let system = format!(
            "You are a skill matcher. Given the user's request, pick the BEST matching skill from the list below, or respond 'none' if no skill fits.\n\n\
             Available skills:\n{options}\n\
             Rules:\n\
             - Only pick a skill if it clearly matches the user's intent\n\
             - If the request is generic and no skill specifically handles it, respond 'none'\n\
             - Respond with ONLY the skill number (1, 2, or 3) or 'none'\n\
             - The user may write in English or Indonesian"
        );

        let request = ChatRequest {
            messages: vec![
                ChatMessage::text("system", system),
                ChatMessage::text("user", user_message),
            ],
            max_tokens: Some(16),
            temperature: Some(0.0),
        };

        let response = self.llm.chat_intent(request).await?;
        let answer = response.content.trim().to_lowercase();

        if answer == "none" {
            return Ok(None);
        }

        // Parse the skill number
        let index: usize = answer
            .chars()
            .find(|c| c.is_ascii_digit())
            .and_then(|c| c.to_digit(10))
            .map(|d| d as usize)
            .unwrap_or(0);

        if index < 1 || index > candidates.len() {
            return Ok(None);
        }

        let skill = candidates.into_iter().nth(index - 1).unwrap();
        let allowed_tools = skill.tools.as_ref().and_then(|t| {
            serde_json::from_str::<Vec<String>>(t).ok()
        });

        Ok(Some(ResolvedSkill { skill, allowed_tools }))
    }
}
