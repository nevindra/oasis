package agent

import (
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/processor"
	"github.com/nevindra/oasis/skills"
)

// Aliases for core protocol types. These mirror the root oasis aliases so that
// the agent files (moved from root in Phase 2.5) can continue to use unqualified
// type names like Provider, ChatMessage, StreamEvent, etc.

// --- Provider / embedding ---

type Provider = core.Provider
type EmbeddingProvider = core.EmbeddingProvider

// --- Tool primitives ---

type AnyTool = core.AnyTool
type StreamingAnyTool = core.StreamingAnyTool
type ToolResult = core.ToolResult
type ToolCall = core.ToolCall
type ToolDefinition = core.ToolDefinition
type ToolRegistry = core.ToolRegistry

// NewToolRegistry creates an empty tool registry. See core.NewToolRegistry.
func NewToolRegistry() *ToolRegistry { return core.NewToolRegistry() }

// --- LLM protocol types ---

type ChatMessage = core.ChatMessage
type ChatRequest = core.ChatRequest
type ChatResponse = core.ChatResponse
type Attachment = core.Attachment
type ResponseSchema = core.ResponseSchema
type GenerationParams = core.GenerationParams
type Usage = core.Usage

// ChatMessage constructors.
func UserMessage(text string) ChatMessage      { return core.UserMessage(text) }
func SystemMessage(text string) ChatMessage    { return core.SystemMessage(text) }
func AssistantMessage(text string) ChatMessage { return core.AssistantMessage(text) }
func ToolResultMessage(callID, content string) ChatMessage {
	return core.ToolResultMessage(callID, content)
}

// --- Streaming ---

type StreamEvent = core.StreamEvent
type StreamEventType = core.StreamEventType

const (
	EventInputReceived   = core.EventInputReceived
	EventProcessingStart = core.EventProcessingStart
	EventTextDelta       = core.EventTextDelta
	EventToolCallStart   = core.EventToolCallStart
	EventToolCallResult  = core.EventToolCallResult
	EventThinking        = core.EventThinking
	EventAgentStart      = core.EventAgentStart
	EventAgentFinish     = core.EventAgentFinish
	EventToolCallDelta   = core.EventToolCallDelta
	EventToolProgress    = core.EventToolProgress
	EventStepStart       = core.EventStepStart
	EventStepFinish      = core.EventStepFinish
	EventStepProgress    = core.EventStepProgress
	EventRoutingDecision = core.EventRoutingDecision
	EventFileAttachment  = core.EventFileAttachment
	EventMaxIterReached  = core.EventMaxIterReached
	EventReasoningStart  = core.EventReasoningStart
	EventReasoningDelta  = core.EventReasoningDelta
	EventReasoningEnd    = core.EventReasoningEnd
	EventHalt            = core.EventHalt
	EventError           = core.EventError
	EventStreamWarning   = core.EventStreamWarning
	EventToolApprovalPending = core.EventToolApprovalPending

	// Lifecycle envelope (Phase 2).
	EventRunStart        = core.EventRunStart
	EventRunFinish       = core.EventRunFinish
	EventIterationStart  = core.EventIterationStart
	EventIterationFinish = core.EventIterationFinish

	// Structured object streaming (Phase 6).
	EventObjectDelta  = core.EventObjectDelta
	EventObjectFinish = core.EventObjectFinish
	EventElementDelta = core.EventElementDelta

	// HITL suspend events.
	EventToolCallSuspended  = core.EventToolCallSuspended
	EventStepSuspended      = core.EventStepSuspended
	EventProcessorSuspended = core.EventProcessorSuspended
)

// FinishReason aliases.
type FinishReason = core.FinishReason

const (
	FinishStop          = core.FinishStop
	FinishToolCalls     = core.FinishToolCalls
	FinishLength        = core.FinishLength
	FinishContentFilter = core.FinishContentFilter
	FinishHalted        = core.FinishHalted
	FinishSuspended     = core.FinishSuspended
	FinishMaxIter       = core.FinishMaxIter
	FinishError         = core.FinishError
)

// --- Processors ---

type PreProcessor = core.PreProcessor
type PostProcessor = core.PostProcessor
type PostToolProcessor = core.PostToolProcessor
type ErrHalt = core.ErrHalt
type ProcessorChain = processor.Chain

// NewProcessorChain creates an empty processor chain.
func NewProcessorChain() *ProcessorChain { return processor.NewChain() }

// --- Compaction ---

type Compactor = core.Compactor

// --- Tracer types ---

type Tracer = core.Tracer
type Span = core.Span
type SpanAttr = core.SpanAttr

// Tracer attribute constructors.
func StringAttr(k, v string) SpanAttr          { return core.StringAttr(k, v) }
func IntAttr(k string, v int) SpanAttr         { return core.IntAttr(k, v) }
func BoolAttr(k string, v bool) SpanAttr       { return core.BoolAttr(k, v) }
func Float64Attr(k string, v float64) SpanAttr { return core.Float64Attr(k, v) }

// NewID generates a globally unique, time-sortable UUIDv7.
func NewID() string { return core.NewID() }

// (Agent, StreamingAgent, AgentTask, AgentResult, StepTrace are aliased in agent.go.)

// IterationTrace and LLMCallTrace are re-exported so agent-internal code
// (iteration.go) can reference them without a core. qualifier.
type IterationTrace = core.IterationTrace
type LLMCallTrace = core.LLMCallTrace

// --- Persistence types used by agent (memory wiring) ---

type Store = core.Store
type Message = core.Message
type Thread = core.Thread
type Fact = core.Fact
type Document = core.Document
type Chunk = core.Chunk
type ChunkFilter = core.ChunkFilter
type ScoredMessage = core.ScoredMessage
type ScoredChunk = core.ScoredChunk
type ScheduledAction = core.ScheduledAction
type ScoredFact = core.ScoredFact

// CosineSimilarity is re-exported from core for compatibility.
var CosineSimilarity = core.CosineSimilarity

// --- Compaction ---

type CompactRequest = core.CompactRequest
type CompactResult = core.CompactResult

// --- Errors ---

type ErrLLM = core.ErrLLM
type ErrHTTP = core.ErrHTTP

// --- Memory ---

type MemoryStore = memory.MemoryStore

// --- Skills ---

type Skill = skills.Skill
type SkillProvider = skills.SkillProvider

// --- Per-request resolution functions ---

// ModelFunc resolves the LLM provider per-request. Re-exported from core.
type ModelFunc = core.ModelFunc
