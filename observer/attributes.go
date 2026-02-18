package observer

import "go.opentelemetry.io/otel/attribute"

// Attribute keys for LLM observability spans and metrics.
var (
	AttrLLMModel    = attribute.Key("llm.model")
	AttrLLMProvider = attribute.Key("llm.provider")
	AttrLLMMethod   = attribute.Key("llm.method")

	AttrTokensInput  = attribute.Key("llm.tokens.input")
	AttrTokensOutput = attribute.Key("llm.tokens.output")
	AttrCostUSD      = attribute.Key("llm.cost_usd")

	AttrToolCount = attribute.Key("llm.tool_count")
	AttrToolNames = attribute.Key("llm.tool_names")

	AttrStreamChunks = attribute.Key("llm.stream_chunks")

	AttrEmbedTextCount  = attribute.Key("llm.embed.text_count")
	AttrEmbedDimensions = attribute.Key("llm.embed.dimensions")

	AttrToolName         = attribute.Key("tool.name")
	AttrToolStatus       = attribute.Key("tool.status")
	AttrToolResultLength = attribute.Key("tool.result_length")

	AttrAgentName   = attribute.Key("agent.name")
	AttrAgentType   = attribute.Key("agent.type")
	AttrAgentStatus = attribute.Key("agent.status")
)
