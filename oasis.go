// Package oasis is the curated public facade of the Oasis framework.
//
// First-time users start here. Construct an Agent with [NewAgent], optionally
// wrap multiple agents with [NewNetwork] or [NewWorkflow], and call Execute.
//
// For features beyond this curated set, import the relevant subpackage directly:
//
//   - github.com/nevindra/oasis/agent       — LLMAgent and its full option set
//   - github.com/nevindra/oasis/network     — Network orchestration
//   - github.com/nevindra/oasis/workflow    — Workflow / DAG orchestration
//   - github.com/nevindra/oasis/memory      — Memory configuration
//   - github.com/nevindra/oasis/skills      — Skill providers
//   - github.com/nevindra/oasis/core        — Protocol types, events, finish reasons
//   - github.com/nevindra/oasis/processor   — Processor chain helper
//   - github.com/nevindra/oasis/ratelimit   — Rate-limited provider wrapper
//   - github.com/nevindra/oasis/provider/*  — Provider implementations
//   - github.com/nevindra/oasis/store/*     — Persistence backends
//
// Adding a re-export here is a deliberate decision: it signals "this is part
// of the curated public surface." Power-user and niche APIs intentionally stay
// in their subpackage.
package oasis

import (
	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/network"
	"github.com/nevindra/oasis/processor"
	"github.com/nevindra/oasis/ratelimit"
	"github.com/nevindra/oasis/skills"
	"github.com/nevindra/oasis/workflow"
)

// --- Core types ---

type Agent = core.Agent
type AgentTask = core.AgentTask
type AgentResult = core.AgentResult
type Provider = core.Provider
type EmbeddingProvider = core.EmbeddingProvider
type AnyTool = core.AnyTool
type Tool[In, Out any] = core.Tool[In, Out]
type ToolResult = core.ToolResult
type RunOption = core.RunOption
type ChatMessage = core.ChatMessage
type ChatRequest = core.ChatRequest
type ChatResponse = core.ChatResponse
type Skill = skills.Skill

// --- Agent option types ---

type Limits = agent.Limits
type Generation = agent.Generation
type Processors = agent.Processors
type Hooks = agent.Hooks
type Stream = agent.Stream
type SuspendProtocol[Req, Resp any] = agent.SuspendProtocol[Req, Resp]
type ErrSuspended = agent.ErrSuspended

// --- Constructors ---

// NewAgent constructs an LLM-driven Agent. See [agent.New] for the full contract.
var NewAgent = agent.New

// NewLLMAgent is the legacy spelling of [NewAgent]. Prefer NewAgent in new code.
//
// Deprecated: use [NewAgent].
var NewLLMAgent = agent.New

// NewNetwork constructs a multi-agent coordinator. See [network.New].
var NewNetwork = network.New

// NewWorkflow constructs a DAG-based agent orchestrator. See [workflow.New].
var NewWorkflow = workflow.New

// NewToolRegistry creates an empty tool registry. See [core.NewToolRegistry].
func NewToolRegistry() *core.ToolRegistry { return core.NewToolRegistry() }

// NewProcessorChain creates an empty processor chain. See [processor.NewChain].
func NewProcessorChain() *processor.Chain { return processor.NewChain() }

// NewSuspendProtocol declares a typed HITL contract. See [agent.NewSuspendProtocol].
func NewSuspendProtocol[Req, Resp any](name string) SuspendProtocol[Req, Resp] {
	return agent.NewSuspendProtocol[Req, Resp](name)
}

// NewInMemoryToolResultStore returns the default in-process ToolResultStore.
var NewInMemoryToolResultStore = core.NewInMemoryToolResultStore

// NewID generates a globally unique, time-sortable UUIDv7 (RFC 9562).
var NewID = core.NewID

// Spawn runs an Agent in the background and returns an [agent.AgentHandle].
var Spawn = agent.Spawn

// Subscribe runs ag with streaming wired up and returns a [Stream] the caller
// may subscribe to or query for the final result. See [agent.Subscribe].
var Subscribe = agent.Subscribe

// --- Agent options (curated) ---

var WithTools = agent.WithTools
var WithPrompt = agent.WithPrompt
var WithMemory = agent.WithMemory
var WithLimits = agent.WithLimits
var WithGeneration = agent.WithGeneration
var WithResponseSchema = agent.WithResponseSchema
var WithDynamicPrompt = agent.WithDynamicPrompt
var WithDynamicModel = agent.WithDynamicModel
var WithDynamicTools = agent.WithDynamicTools
var WithTracer = agent.WithTracer
var WithLogger = agent.WithLogger
var WithMetadata = agent.WithMetadata
var WithProcessors = agent.WithProcessors
var WithHooks = agent.WithHooks
var WithToolConfig = agent.WithToolConfig
var Approval = agent.Approval
var WithInputHandler = agent.WithInputHandler
var WithMiddleware = agent.WithMiddleware
var WithSkills = agent.WithSkills
var WithActiveSkills = agent.WithActiveSkills
var WithEmbedding = agent.WithEmbedding
var RetryMiddleware = agent.RetryMiddleware
var WithOverrides = agent.WithOverrides

// --- Run options (per-call) ---

var WithStream = core.WithStream
var WithDeadline = core.WithDeadline

// --- Provider wrappers ---

var WithRateLimit = ratelimit.WithRateLimit
var RPM = ratelimit.RPM

// --- Tool helpers ---

// Erase converts a typed [Tool] into [AnyTool]. See [core.Erase].
func Erase[In, Out any](t core.Tool[In, Out]) core.AnyTool { return core.Erase(t) }
