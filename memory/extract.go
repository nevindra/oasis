// Package memory provides storage-agnostic helpers for user memory extraction.
// Use these with any oasis.MemoryStore implementation.
package memory

import (
	"encoding/json"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// ExtractedFact is a parsed fact from LLM extraction.
type ExtractedFact struct {
	Fact       string  `json:"fact"`
	Category   string  `json:"category"`
	Supersedes *string `json:"supersedes,omitempty"`
}

// ExtractFactsSchema is the JSON Schema for fact extraction responses.
var ExtractFactsSchema = &oasis.ResponseSchema{
	Name:   "extracted_facts",
	Schema: json.RawMessage(`{"type":"array","items":{"type":"object","properties":{"fact":{"type":"string"},"category":{"type":"string","enum":["personal","preference","work","habit","relationship"]},"supersedes":{"type":"string"}},"required":["fact","category"]}}`),
}

// ExtractFactsPrompt is the system prompt for fact extraction.
const ExtractFactsPrompt = `You are a memory extraction system. Given a conversation between a user and an assistant, extract factual information ABOUT THE USER.

Extract facts like:
- Personal info (name, job, location, timezone)
- Preferences (communication style, tools, languages)
- Habits and routines
- Current projects or goals
- Relationships and people they mention

Rules:
- Only extract facts clearly stated or strongly implied by the USER (not the assistant)
- Each fact should be a single, concise statement
- Categorize each fact as: personal, preference, work, habit, or relationship
- If a new fact CONTRADICTS or UPDATES a previously known fact, include a "supersedes" field with the old fact text
- If no new user facts are present, return an empty array
- Do NOT extract facts about the assistant or general knowledge

Return a JSON array:
[{"fact": "User moved to Bali", "category": "personal", "supersedes": "Lives in Jakarta"}]

If the fact does not supersede anything, omit the "supersedes" field:
[{"fact": "User's name is Nev", "category": "personal"}]

Return ONLY the JSON array, no extra text. Return [] if no facts found.`

// ShouldExtract returns true if the message is worth running fact extraction on.
func ShouldExtract(text string) bool {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 10 {
		return false
	}
	lower := strings.ToLower(trimmed)
	skip := []string{
		"ok", "oke", "okay", "okey",
		"thanks", "thank you", "makasih", "thx", "ty",
		"yes", "no", "ya", "ga", "gak", "nggak", "engga",
		"nice", "sip", "siap", "oke sip",
		"lol", "haha", "wkwk", "wkwkwk",
		"hmm", "hm", "oh", "ah",
		"good", "great", "cool", "yep", "nope",
	}
	for _, s := range skip {
		if lower == s {
			return false
		}
	}
	return true
}

// ParseExtractedFacts parses the LLM's fact extraction response.
// Handles both raw JSON arrays and markdown-fenced responses (```json...```).
func ParseExtractedFacts(response string) []ExtractedFact {
	response = strings.TrimSpace(response)
	var facts []ExtractedFact
	if err := json.Unmarshal([]byte(response), &facts); err != nil {
		// LLM sometimes wraps JSON in markdown fences — find the array.
		start := strings.Index(response, "[")
		end := strings.LastIndex(response, "]")
		if start >= 0 && end > start {
			_ = json.Unmarshal([]byte(response[start:end+1]), &facts)
		}
	}
	return facts
}
