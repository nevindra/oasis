//go:generate go run ../../cmd/modelgen

package catalog

import oasis "github.com/nevindra/oasis"

// protocolOverrides maps provider names that don't use OpenAI-compatible protocol.
var protocolOverrides = map[string]oasis.Protocol{
	"gemini": oasis.ProtocolGemini,
}

// generatedPlatforms is overridden by platforms_gen.go when present.
// This fallback ensures the build succeeds before `go generate` runs.
var generatedPlatforms []oasis.Platform

// builtinPlatforms is the manually curated list of known platforms.
// These take precedence over generatedPlatforms for Protocol and BaseURL.
var builtinPlatforms = []oasis.Platform{
	{Name: "OpenAI", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.openai.com/v1", EnvVars: []string{"OPENAI_API_KEY"}},
	{Name: "Gemini", Protocol: oasis.ProtocolGemini, BaseURL: "https://generativelanguage.googleapis.com/v1beta", EnvVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
	{Name: "Groq", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.groq.com/openai/v1", EnvVars: []string{"GROQ_API_KEY"}},
	{Name: "DeepSeek", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.deepseek.com", EnvVars: []string{"DEEPSEEK_API_KEY"}},
	{Name: "Qwen", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", EnvVars: []string{"DASHSCOPE_API_KEY"}},
	{Name: "Qwen-CN", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", EnvVars: []string{"DASHSCOPE_API_KEY"}},
	{Name: "Together", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.together.xyz/v1", EnvVars: []string{"TOGETHER_API_KEY"}},
	{Name: "Mistral", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.mistral.ai/v1", EnvVars: []string{"MISTRAL_API_KEY"}},
	{Name: "Fireworks", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.fireworks.ai/inference/v1", EnvVars: []string{"FIREWORKS_API_KEY"}},
	{Name: "Cerebras", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "https://api.cerebras.ai/v1", EnvVars: []string{"CEREBRAS_API_KEY"}},
	{Name: "Ollama", Protocol: oasis.ProtocolOpenAICompat, BaseURL: "http://localhost:11434/v1"},
}
