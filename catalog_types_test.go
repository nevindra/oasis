package oasis

import "testing"

func TestModelCapabilitiesFields(t *testing.T) {
	caps := ModelCapabilities{
		Chat: true, Vision: true, ToolUse: true, Embedding: true,
		Reasoning: true, StructuredOutput: true, Attachment: true,
	}
	if !caps.Reasoning {
		t.Error("Reasoning field not set")
	}
	if !caps.StructuredOutput {
		t.Error("StructuredOutput field not set")
	}
	if !caps.Attachment {
		t.Error("Attachment field not set")
	}
}

func TestModelPricingCacheFields(t *testing.T) {
	p := ModelPricing{
		InputPerMillion: 1.25, OutputPerMillion: 10.00,
		CacheReadPerMillion: 0.075, CacheWritePerMillion: 0.30,
	}
	if p.CacheReadPerMillion != 0.075 {
		t.Errorf("CacheReadPerMillion = %f, want 0.075", p.CacheReadPerMillion)
	}
	if p.CacheWritePerMillion != 0.30 {
		t.Errorf("CacheWritePerMillion = %f, want 0.30", p.CacheWritePerMillion)
	}
}

func TestModelInfoNewFields(t *testing.T) {
	m := ModelInfo{
		ID: "gpt-4o", Provider: "openai", Family: "gpt-4",
		InputModalities: []string{"text", "image", "audio"}, OutputModalities: []string{"text"},
		OpenWeights: false, KnowledgeCutoff: "2024-10", ReleaseDate: "2024-05-13",
	}
	if m.Family != "gpt-4" {
		t.Errorf("Family = %q, want 'gpt-4'", m.Family)
	}
	if len(m.InputModalities) != 3 {
		t.Errorf("InputModalities len = %d, want 3", len(m.InputModalities))
	}
	if m.KnowledgeCutoff != "2024-10" {
		t.Errorf("KnowledgeCutoff = %q, want '2024-10'", m.KnowledgeCutoff)
	}
}

func TestPlatformEnvVars(t *testing.T) {
	p := Platform{
		Name: "OpenAI", Protocol: ProtocolOpenAICompat,
		BaseURL: "https://api.openai.com/v1", EnvVars: []string{"OPENAI_API_KEY"},
	}
	if len(p.EnvVars) != 1 || p.EnvVars[0] != "OPENAI_API_KEY" {
		t.Errorf("EnvVars = %v, want [OPENAI_API_KEY]", p.EnvVars)
	}
}
