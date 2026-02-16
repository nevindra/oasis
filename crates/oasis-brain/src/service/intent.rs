use serde::{Deserialize, Serialize};

/// Simplified intent: is the user chatting or requesting an action?
///
/// When the intent is `Action`, Brain enters the tool execution loop
/// where the main LLM decides which tools to call.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub enum Intent {
    /// Pure conversation — no tools needed.
    Chat,
    /// The user wants something done — enter tool execution loop.
    Action,
}

/// Parse an LLM response into a structured Intent.
///
/// The LLM is expected to return a JSON object with an "intent" field.
/// If parsing fails, defaults to `Action` (let the tool-use LLM decide).
pub fn parse_intent(llm_response: &str) -> Intent {
    let json_str = extract_json(llm_response);

    let parsed: serde_json::Value = match serde_json::from_str(json_str) {
        Ok(v) => v,
        Err(_) => return Intent::Action,
    };

    match parsed["intent"].as_str() {
        Some("chat") => Intent::Chat,
        _ => Intent::Action,
    }
}

/// Extract a JSON object from a string that may contain surrounding text
/// or markdown code fences.
fn extract_json(input: &str) -> &str {
    let trimmed = input.trim();

    // Strip markdown code fences if present
    let stripped = if trimmed.starts_with("```json") {
        trimmed
            .strip_prefix("```json")
            .unwrap_or(trimmed)
            .strip_suffix("```")
            .unwrap_or(trimmed)
            .trim()
    } else if trimmed.starts_with("```") {
        trimmed
            .strip_prefix("```")
            .unwrap_or(trimmed)
            .strip_suffix("```")
            .unwrap_or(trimmed)
            .trim()
    } else {
        trimmed
    };

    // Find the first '{' and last '}' to extract the JSON object
    if let Some(start) = stripped.find('{') {
        if let Some(end) = stripped.rfind('}') {
            if end >= start {
                return &stripped[start..=end];
            }
        }
    }

    stripped
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_chat_intent() {
        let response = r#"{"intent":"chat"}"#;
        assert!(matches!(parse_intent(response), Intent::Chat));
    }

    #[test]
    fn test_parse_action_intent() {
        let response = r#"{"intent":"action"}"#;
        assert!(matches!(parse_intent(response), Intent::Action));
    }

    #[test]
    fn test_parse_invalid_json_defaults_to_action() {
        let response = "not json at all";
        assert!(matches!(parse_intent(response), Intent::Action));
    }

    #[test]
    fn test_parse_json_in_code_fence() {
        let response = "```json\n{\"intent\":\"chat\"}\n```";
        assert!(matches!(parse_intent(response), Intent::Chat));
    }

    #[test]
    fn test_parse_unknown_intent_defaults_to_action() {
        let response = r#"{"intent":"unknown_type"}"#;
        assert!(matches!(parse_intent(response), Intent::Action));
    }
}
