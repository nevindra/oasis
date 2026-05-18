// Package oasis is the public umbrella for the Oasis agent framework.
//
// This file curates the re-export surface: users import a single package
// (github.com/nevindra/oasis) and get the common API via aliases.
//
// Niche or power-user APIs are deliberately NOT re-exported — those callers
// import the relevant subpackage directly (e.g. "github.com/nevindra/oasis/compaction").
//
// Adding a re-export here is a deliberate decision: it signals "this is part
// of the curated public surface." Do not auto-mirror every new export in a
// subpackage.
package oasis

import (
	"context"
	"time"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/compaction"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/guardrail"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/network"
	"github.com/nevindra/oasis/processor"
	"github.com/nevindra/oasis/ratelimit"
	"github.com/nevindra/oasis/skills"
	"github.com/nevindra/oasis/workflow"
)

// --- Agent ---

// LLMAgent is the canonical LLM-driven Agent implementation. It supports
// tool calling, memory, processors, compaction, suspend/resume, and skills.
type LLMAgent = agent.LLMAgent

// AgentOption configures an LLMAgent or Network at construction.
type AgentOption = agent.AgentOption

// AgentHandle is the asynchronous handle returned by Spawn.
type AgentHandle = agent.AgentHandle

// NewLLMAgent constructs an LLMAgent with the given provider and options.
var NewLLMAgent = agent.NewLLMAgent

// Spawn runs an Agent in the background and returns a handle.
var Spawn = agent.Spawn

// --- Agent options (curated public surface) ---

var WithTools = agent.WithTools
var WithPrompt = agent.WithPrompt
var WithMaxIter = agent.WithMaxIter
var WithMaxAttachmentBytes = agent.WithMaxAttachmentBytes
var WithSuspendBudget = agent.WithSuspendBudget
var WithCompressModel = agent.WithCompressModel
var WithCompressThreshold = agent.WithCompressThreshold
var WithTemperature = agent.WithTemperature
var WithTopP = agent.WithTopP
var WithTopK = agent.WithTopK
var WithMaxTokens = agent.WithMaxTokens
var WithAgents = agent.WithAgents
var WithPlanExecution = agent.WithPlanExecution
var WithSandbox = agent.WithSandbox
var WithSubAgentSpawning = agent.WithSubAgentSpawning
var MaxSpawnDepth = agent.MaxSpawnDepth
var DenySpawnTools = agent.DenySpawnTools
var WithActiveSkills = agent.WithActiveSkills
var WithSkills = agent.WithSkills
var WithResponseSchema = agent.WithResponseSchema
var WithDynamicPrompt = agent.WithDynamicPrompt
var WithDynamicModel = agent.WithDynamicModel
var WithDynamicTools = agent.WithDynamicTools
var WithTracer = agent.WithTracer
var WithLogger = agent.WithLogger
var WithProcessors = agent.WithProcessors
var WithInputHandler = agent.WithInputHandler

// --- Network ---

// Network is a multi-agent coordinator that routes tasks to subagents via
// an LLM router.
type Network = network.Network

// NewNetwork constructs a Network with the given router provider and options.
var NewNetwork = network.NewNetwork


// --- Compaction ---

// NewStructuredCompactor creates the default Compactor implementation that
// turns long conversation histories into structured 9-section summaries via
// a single LLM call. See github.com/nevindra/oasis/compaction for the full API.
var NewStructuredCompactor = compaction.NewStructuredCompactor

// --- Guardrail ---

// NewInjectionGuard creates a PreProcessor that detects and blocks prompt
// injection attempts. See github.com/nevindra/oasis/guardrail for options.
var NewInjectionGuard = guardrail.NewInjectionGuard

// NewContentGuard creates a guard that enforces character length limits on
// input and output content. See github.com/nevindra/oasis/guardrail for options.
var NewContentGuard = guardrail.NewContentGuard

// NewKeywordGuard creates a guard that blocks messages containing specified
// keywords or regex patterns. See github.com/nevindra/oasis/guardrail for options.
var NewKeywordGuard = guardrail.NewKeywordGuard

// --- Rate limiting ---

// WithRateLimit wraps a Provider with proactive rate limiting.
// Compose with RPM and TPM options:
//
//	limited := oasis.WithRateLimit(provider, oasis.RPM(60), oasis.TPM(100_000))
//
// See github.com/nevindra/oasis/ratelimit for the full API.
var WithRateLimit = ratelimit.WithRateLimit

// RPM sets the maximum requests per minute for a rate-limited Provider.
var RPM = ratelimit.RPM

// TPM sets the maximum tokens per minute for a rate-limited Provider.
var TPM = ratelimit.TPM

// --- Workflow ---

// Workflow is a DAG-based agent orchestrator. It satisfies core.Agent so
// workflows can be nested or used wherever an Agent is expected.
type Workflow = workflow.Workflow

// (WorkflowContext is re-exported from suspend.go.)

// WorkflowOption configures a Workflow at construction time.
type WorkflowOption = workflow.WorkflowOption

// StepOption configures an individual workflow step.
type StepOption = workflow.StepOption

// StepFunc is the signature for a workflow step body.
type StepFunc = workflow.StepFunc

// WorkflowResult is the aggregate output of a workflow run.
type WorkflowResult = workflow.WorkflowResult

// WorkflowError is returned by Workflow.Execute when a step fails.
type WorkflowError = workflow.WorkflowError

// NewWorkflow constructs a workflow from the supplied options.
var NewWorkflow = workflow.NewWorkflow

// Step registers a named step in the workflow.
var Step = workflow.Step

// AgentStep delegates a step's work to the given Agent.
var AgentStep = workflow.AgentStep

// ToolStep invokes a tool as a workflow step.
var ToolStep = workflow.ToolStep

// ForEach iterates over a collection produced by an earlier step.
var ForEach = workflow.ForEach

// After declares the upstream dependencies of a step.
var After = workflow.After

// When gates a step on a runtime predicate.
var When = workflow.When

// InputFrom maps a step's input from a key in WorkflowContext.
var InputFrom = workflow.InputFrom

// OutputTo writes a step's result into the given key.
var OutputTo = workflow.OutputTo

// Retry sets per-step retry behavior.
var Retry = workflow.Retry

// IterOver makes a step iterate over the collection at the given context key.
var IterOver = workflow.IterOver

// WithOnFinish registers a workflow-level completion callback.
var WithOnFinish = workflow.WithOnFinish

// WithOnError registers a per-step error handler.
var WithOnError = workflow.WithOnError

// --- Network ---

// Network is a multi-agent coordinator that routes tasks to subagents.
// Import github.com/nevindra/oasis/network directly for the full API.
//
// Example:
//
//	import "github.com/nevindra/oasis/network"
//	...
//	net := network.NewNetwork("coordinator", "...", router, oasis.WithAgents(...))

// --- Memory ---

// MemoryStore is the interface for long-term semantic memory backed by an
// embedding provider. Manually imported from the memory subpackage:
//
//	import "github.com/nevindra/oasis/memory"
//	...
//	agent := oasis.NewLLMAgent(name, desc, provider,
//		oasis.WithUserMemory(memoryStore, embedding),
//		oasis.WithCrossThreadSearch(embedding),
//	)
//
// See github.com/nevindra/oasis/memory for the full API.
type MemoryStore = memory.MemoryStore

// --- Skills ---

// Skills are managed via github.com/nevindra/oasis/skills subpackage.
// Import it directly for skill discovery and loading:
//
//	import "github.com/nevindra/oasis/skills"
//	provider := skills.NewFileSkillProvider(dirs...)
//	agent := oasis.NewLLMAgent(name, desc, provider, oasis.WithSkills(provider))
//
// See github.com/nevindra/oasis/skills for the full API.
type SkillProvider = skills.SkillProvider
type SkillWriter = skills.SkillWriter
type SkillSummary = skills.SkillSummary
type Skill = skills.Skill
type FileSkillProvider = skills.FileSkillProvider
type ChainedSkillProvider = skills.ChainedSkillProvider
type BuiltinSkillProvider = skills.BuiltinSkillProvider

// NewFileSkillProvider creates a skill provider that loads SKILL.md files
// from the given directories. Falls back to DefaultSkillDirs() when no dirs
// are given.
func NewFileSkillProvider(dirs ...string) *FileSkillProvider {
	return skills.NewFileSkillProvider(dirs...)
}

// ChainSkillProviders merges multiple SkillProviders.
func ChainSkillProviders(providers ...SkillProvider) *ChainedSkillProvider {
	return skills.ChainSkillProviders(providers...)
}

// ActivateWithReferences loads a skill and recursively loads all referenced
// skills, appending their instructions to the root skill's instructions.
func ActivateWithReferences(ctx context.Context, p SkillProvider, name string) (Skill, error) {
	return skills.ActivateWithReferences(ctx, p, name)
}

// NewBuiltinSkillProvider returns a provider that reads the framework's
// embedded skills.
func NewBuiltinSkillProvider() *BuiltinSkillProvider {
	return skills.NewBuiltinSkillProvider()
}

// DefaultSkillDirs returns the standard AgentSkills-compatible scan paths
// (project-level <cwd>/.agents/skills/ and user-level ~/.agents/skills/).
// Missing directories are tolerated by FileSkillProvider.
func DefaultSkillDirs() []string { return skills.DefaultSkillDirs() }

// --- Workflow definition types ---

type NodeType = workflow.NodeType

const (
	NodeLLM       NodeType = workflow.NodeLLM
	NodeTool      NodeType = workflow.NodeTool
	NodeCondition NodeType = workflow.NodeCondition
	NodeTemplate  NodeType = workflow.NodeTemplate
)

type WorkflowDefinition = workflow.WorkflowDefinition
type NodeDefinition = workflow.NodeDefinition
type DefinitionRegistry = workflow.DefinitionRegistry

// --- Helpers ---

// NewID generates a globally unique, time-sortable UUIDv7 (RFC 9562).
func NewID() string { return core.NewID() }

// NowUnix returns current time as Unix seconds.
func NowUnix() int64 { return core.NowUnix() }

// --- Provider / embedding ---

type Provider = core.Provider
type EmbeddingProvider = core.EmbeddingProvider
type MultimodalInput = core.MultimodalInput
type MultimodalEmbeddingProvider = core.MultimodalEmbeddingProvider
type BlobStore = core.BlobStore

// --- Tool primitives ---

type AnyTool = core.AnyTool
type StreamingAnyTool = core.StreamingAnyTool

// Tool re-exports core.Tool. Generic type aliases are supported as of Go 1.24
// (fully stabilised in 1.25), so callers may continue to write oasis.Tool[In, Out].
type Tool[In, Out any] = core.Tool[In, Out]

type ToolResult = core.ToolResult
type ToolCall = core.ToolCall
type ToolDefinition = core.ToolDefinition
type ToolRegistry = core.ToolRegistry
type SchemaEnsurer = core.SchemaEnsurer

// NewToolRegistry creates an empty registry. See core.NewToolRegistry.
func NewToolRegistry() *ToolRegistry { return core.NewToolRegistry() }

// Erase converts a Tool[In, Out] into AnyTool. Forwards to core.Erase.
func Erase[In, Out any](t core.Tool[In, Out]) core.AnyTool {
	return core.Erase(t)
}

// --- LLM protocol types ---

type ChatMessage = core.ChatMessage
type ChatRequest = core.ChatRequest
type ChatResponse = core.ChatResponse
type Attachment = core.Attachment
type ResponseSchema = core.ResponseSchema
type SchemaObject = core.SchemaObject
type GenerationParams = core.GenerationParams
type Usage = core.Usage

// NewResponseSchema creates a ResponseSchema by marshalling a SchemaObject.
func NewResponseSchema(name string, s *SchemaObject) *ResponseSchema {
	return core.NewResponseSchema(name, s)
}

// UserMessage, SystemMessage, AssistantMessage, ToolResultMessage construct
// ChatMessage values with the appropriate role.
func UserMessage(text string) ChatMessage      { return core.UserMessage(text) }
func SystemMessage(text string) ChatMessage    { return core.SystemMessage(text) }
func AssistantMessage(text string) ChatMessage { return core.AssistantMessage(text) }
func ToolResultMessage(callID, content string) ChatMessage {
	return core.ToolResultMessage(callID, content)
}

// --- Streaming ---

type StreamEventType = core.StreamEventType
type StreamEvent = core.StreamEvent

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
)

// --- Processors ---

type PreProcessor = core.PreProcessor
type PostProcessor = core.PostProcessor
type PostToolProcessor = core.PostToolProcessor
type ErrHalt = core.ErrHalt

// ProcessorChain is the standard composition helper for chaining processors.
type ProcessorChain = processor.Chain

// NewProcessorChain creates an empty ProcessorChain.
func NewProcessorChain() *ProcessorChain { return processor.NewChain() }

// --- Compaction ---

type Compactor = core.Compactor
type CompactRequest = core.CompactRequest
type CompactSection = core.CompactSection
type CompactResult = core.CompactResult

// --- Catalog vocabulary ---

type Protocol = core.Protocol
type Platform = core.Platform
type ModelInfo = core.ModelInfo
type ModelCapabilities = core.ModelCapabilities
type ModelPricing = core.ModelPricing
type ModelStatus = core.ModelStatus

const (
	ProtocolOpenAICompat = core.ProtocolOpenAICompat
	ProtocolGemini       = core.ProtocolGemini

	ModelStatusUnknown     = core.ModelStatusUnknown
	ModelStatusAvailable   = core.ModelStatusAvailable
	ModelStatusUnavailable = core.ModelStatusUnavailable
)

// ParseModelID splits a "provider/model" string into provider and model parts.
func ParseModelID(id string) (provider, model string) { return core.ParseModelID(id) }

// --- Errors ---

type ErrLLM = core.ErrLLM
type ErrHTTP = core.ErrHTTP

// ParseRetryAfter parses a Retry-After header value into a duration.
func ParseRetryAfter(value string) time.Duration { return core.ParseRetryAfter(value) }

// --- Agent core types ---

type Agent = core.Agent
type StreamingAgent = core.StreamingAgent
type AgentTask = core.AgentTask
type AgentResult = core.AgentResult
type StepTrace = core.StepTrace

// Context key constants for AgentTask.Context (exported for subpackage access).
const (
	ContextThreadID = core.ContextThreadID
	ContextUserID   = core.ContextUserID
	ContextChatID   = core.ContextChatID
)

// --- Tracer types ---

type Tracer = core.Tracer
type Span = core.Span
type SpanAttr = core.SpanAttr

func StringAttr(k, v string) SpanAttr           { return core.StringAttr(k, v) }
func IntAttr(k string, v int) SpanAttr          { return core.IntAttr(k, v) }
func BoolAttr(k string, v bool) SpanAttr        { return core.BoolAttr(k, v) }
func Float64Attr(k string, v float64) SpanAttr  { return core.Float64Attr(k, v) }

// --- Persistence types ---

type Store = core.Store
type Thread = core.Thread
type Message = core.Message
type Fact = core.Fact
type Document = core.Document
type Chunk = core.Chunk
type ChunkMeta = core.ChunkMeta
type Image = core.Image
type RelationType = core.RelationType
type ChunkEdge = core.ChunkEdge
type ChunkFilter = core.ChunkFilter
type FilterOp = core.FilterOp
type ScoredMessage = core.ScoredMessage
type ScoredChunk = core.ScoredChunk
type ScoredFact = core.ScoredFact
type ScheduledAction = core.ScheduledAction
type ScheduledToolCall = core.ScheduledToolCall

const (
	RelReferences  = core.RelReferences
	RelElaborates  = core.RelElaborates
	RelDependsOn   = core.RelDependsOn
	RelContradicts = core.RelContradicts
	RelPartOf      = core.RelPartOf
	RelSimilarTo   = core.RelSimilarTo
	RelSequence    = core.RelSequence
	RelCausedBy    = core.RelCausedBy

	OpEq  = core.OpEq
	OpIn  = core.OpIn
	OpGt  = core.OpGt
	OpLt  = core.OpLt
	OpNeq = core.OpNeq
)

func ByDocument(ids ...string) ChunkFilter    { return core.ByDocument(ids...) }
func BySource(s string) ChunkFilter           { return core.BySource(s) }
func ByMeta(k, v string) ChunkFilter          { return core.ByMeta(k, v) }
func ByExcludeDocument(id string) ChunkFilter { return core.ByExcludeDocument(id) }
func CreatedAfter(u int64) ChunkFilter        { return core.CreatedAfter(u) }
func CreatedBefore(u int64) ChunkFilter       { return core.CreatedBefore(u) }

// CosineSimilarity computes the cosine similarity between two float32 vectors.
// Thin re-export of core.CosineSimilarity.
func CosineSimilarity(a, b []float32) float32 { return core.CosineSimilarity(a, b) }

// --- Store capability interfaces ---

type KeywordSearcher = core.KeywordSearcher
type GraphStore = core.GraphStore
type BidirectionalGraphStore = core.BidirectionalGraphStore
type DocumentGetter = core.DocumentGetter
type DocumentMetaLister = core.DocumentMetaLister

// --- Ingest checkpoint types ---

type IngestCheckpoint = core.IngestCheckpoint
type CheckpointStatus = core.CheckpointStatus
type CheckpointStore = core.CheckpointStore

const (
	CheckpointExtracting = core.CheckpointExtracting
	CheckpointChunking   = core.CheckpointChunking
	CheckpointEnriching  = core.CheckpointEnriching
	CheckpointEmbedding  = core.CheckpointEmbedding
	CheckpointStoring    = core.CheckpointStoring
	CheckpointGraphing   = core.CheckpointGraphing
)
