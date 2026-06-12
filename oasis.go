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
//   - github.com/nevindra/oasis/core        — Full protocol type set (common types re-exported above)
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
	"context"

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
type UIComponent = core.UIComponent
type UIRenderable = core.UIRenderable
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

// --- Protocol types ---

type Store                = core.Store
type ScheduledActionStore = core.ScheduledActionStore
type ToolDefinition       = core.ToolDefinition
type StreamEvent      = core.StreamEvent
type StreamEventType  = core.StreamEventType
type FinishReason     = core.FinishReason
type InputHandler     = agent.InputHandler

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

// --- Stream event types (curated — niche events stay in core) ---

var (
	EventTextDelta       = core.EventTextDelta
	EventToolCallStart   = core.EventToolCallStart
	EventToolCallResult  = core.EventToolCallResult
	EventUIComponent     = core.EventUIComponent
	EventToolCallDelta   = core.EventToolCallDelta
	EventToolProgress    = core.EventToolProgress
	EventAgentStart      = core.EventAgentStart
	EventAgentFinish     = core.EventAgentFinish
	EventRoutingDecision = core.EventRoutingDecision
	EventThinking        = core.EventThinking
	EventFileAttachment  = core.EventFileAttachment
	EventRunStart        = core.EventRunStart
	EventRunFinish       = core.EventRunFinish
	EventIterationStart  = core.EventIterationStart
	EventIterationFinish = core.EventIterationFinish
	EventError           = core.EventError
)

// AllStreamEventTypes returns every StreamEventType constant defined by the
// framework. See [core.AllStreamEventTypes].
var AllStreamEventTypes = core.AllStreamEventTypes

// --- Finish reasons ---

var (
	FinishStop          = core.FinishStop
	FinishToolCalls     = core.FinishToolCalls
	FinishLength        = core.FinishLength
	FinishContentFilter = core.FinishContentFilter
	FinishHalted        = core.FinishHalted
	FinishSuspended     = core.FinishSuspended
	FinishMaxIter       = core.FinishMaxIter
	FinishError         = core.FinishError
)

// --- Message constructors ---

var (
	SystemMessage    = core.SystemMessage
	UserMessage      = core.UserMessage
	AssistantMessage = core.AssistantMessage
)

// --- Additional agent options ---

var WithSandbox             = agent.WithSandbox
var InputHandlerFromContext = agent.InputHandlerFromContext

// --- Convenience functions ---

// Chat is a non-streaming convenience wrapper around Provider.ChatStream.
// It discards stream events and returns the final assembled response.
var Chat = core.Chat

// --- Provider wrappers ---

var WithRateLimit = ratelimit.WithRateLimit
var RPM = ratelimit.RPM

// --- Tool helpers ---

// Func creates an [AnyTool] from a plain function. Schema is derived from In
// by reflection; Out is marshaled to JSON on each call. See [core.Func].
func Func[In, Out any](name, desc string, fn func(context.Context, In) (Out, error)) AnyTool {
	return core.Func[In, Out](name, desc, fn)
}

// Erase converts a typed [Tool] into [AnyTool]. See [core.Erase].
func Erase[In, Out any](t core.Tool[In, Out]) core.AnyTool { return core.Erase(t) }

// UIResult re-exports core.UIResult: build a ToolResult that renders as the
// named frontend component.
func UIResult[T any](name string, props T) core.ToolResult { return core.UIResult(name, props) }

// TextResult is a convenience for tools producing plain text. See [core.TextResult].
var TextResult = core.TextResult

// JSONResult marshals v into a ToolResult. See [core.JSONResult].
func JSONResult[T any](v T) ToolResult { return core.JSONResult(v) }

// ErrorResult returns a ToolResult with the Error field set. See [core.ErrorResult].
var ErrorResult = core.ErrorResult

// RawTool creates an AnyTool from a name, description, JSON schema, and raw
// execution function. See [core.RawTool].
var RawTool = core.RawTool
