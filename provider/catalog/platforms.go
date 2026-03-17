package catalog

import oasis "github.com/nevindra/oasis"

// builtinPlatforms is the list of known LLM provider platforms.
// Updated with each Oasis release. End users see these in the UI
// without any developer code. Custom platforms can be registered
// at runtime via ModelCatalog.RegisterPlatform.
var builtinPlatforms = []oasis.Platform{
	{Name: "OpenAI", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.openai.com/v1"},
	{Name: "Gemini", Protocol: oasis.ProtocolGemini, BaseURL: "https://generativelanguage.googleapis.com/v1beta"},
	{Name: "Groq", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.groq.com/openai/v1"},
	{Name: "DeepSeek", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.deepseek.com"},
	{Name: "Qwen", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
	{Name: "Together", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.together.xyz/v1"},
	{Name: "Mistral", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.mistral.ai/v1"},
	{Name: "Fireworks", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.fireworks.ai/inference/v1"},
	{Name: "Cerebras", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.cerebras.ai/v1"},
	{Name: "Ollama", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "http://localhost:11434/v1"},
}
