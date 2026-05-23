package agent

import "github.com/nevindra/oasis/core"

// Exported for tests: invoke compressMessages directly.
var TestCompressMessages = compressMessages

// AgentToolResultStore exposes the tool result store of an LLMAgent for testing.
func AgentToolResultStore(a *LLMAgent) core.ToolResultStore {
	return a.ToolResultStore
}

// SplitContentRunes exposes the internal chunking helper for unit tests.
var SplitContentRunes = splitContentRunes
