package observer

import "go.opentelemetry.io/otel/attribute"

// Attribute keys for LLM observability spans and metrics.
var (
	// llm.model_name / llm.provider match OpenInference so Arize resolves the
	// model on generation spans; the same keys double as metric labels.
	AttrLLMModel    = attribute.Key("llm.model_name")
	AttrLLMProvider = attribute.Key("llm.provider")
	AttrLLMMethod   = attribute.Key("llm.method")

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
