package runtime

import (
	"sync"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/processor"
)

// LoopConfig holds everything the shared runLoop needs to run.
//
// Config is embedded by value so that runLoop accesses overridable knobs
// (MaxIter, Logger, Tracer, GenParams, PrepareStep, etc.) via field promotion.
// BaseLoopConfig assigns lc.Config = *cfg once. Adding a new per-call-
// overridable field only requires adding it to Config and to ApplyRunOptions;
// LoopConfig and BaseLoopConfig pick it up automatically.
//
// The direct fields below are runtime-only: computed per call or referencing
// shared mutable state from Runtime.
type LoopConfig struct {
	Config // embedded — option-set fields promote up

	// Identity and per-call runtime wiring.
	Name           string // for logging (e.g. "agent:foo", "network:bar")
	Provider       core.Provider
	Tools          []core.ToolDefinition // pre-built tool defs (including built-ins)
	Processors     *processor.Chain
	Mem            *memory.AgentMemory
	Dispatch       DispatchFunc
	SystemPrompt   string             // resolved (post-dynamic), shadows Config.SystemPrompt
	ResumeMessages []core.ChatMessage // if set, replaces buildMessages (suspend/resume)

	// Suspend budget — pointers into Runtime's shared counters; nil = no tracking.
	SuspendCount *int64
	SuspendBytes *int64
	SuspendMu    *sync.Mutex

	// Compressor is the per-turn tool-result compressor.
	Compressor core.Compactor

	// MaxStepsResolved shadows Config.MaxSteps (*int), dereferenced to int.
	// 0 = unbounded.
	MaxStepsResolved int

	// LookupTool resolves a registered tool by name for source aggregation.
	LookupTool func(string) (core.AnyTool, bool)
}
