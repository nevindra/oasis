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
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/compaction"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/guardrail"
	"github.com/nevindra/oasis/history"
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

// Limits groups all per-agent resource-budget fields into a single value.
// Use WithLimits to apply it.
type Limits = agent.Limits

// Unbounded is the sentinel for limit fields where 0 already means "use
// default". Pass agent.Unbounded (or oasis.Unbounded) as the MaxSteps field
// in Limits to mean "no cap".
const Unbounded = agent.Unbounded

// WithLimits sets the agent's resource-budget Limits in one option call.
var WithLimits = agent.WithLimits

// Typed HITL contracts — see docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md
type SuspendProtocol[Req, Resp any] = agent.SuspendProtocol[Req, Resp]
type ErrSuspended = agent.ErrSuspended
var Suspend = agent.Suspend

// NewSuspendProtocol declares a typed HITL contract. See agent.NewSuspendProtocol.
func NewSuspendProtocol[Req, Resp any](name string) SuspendProtocol[Req, Resp] {
	return agent.NewSuspendProtocol[Req, Resp](name)
}

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
var WithPreProcessors = agent.WithPreProcessors
var WithPostProcessors = agent.WithPostProcessors
var WithPostToolProcessors = agent.WithPostToolProcessors
var WithInputHandler = agent.WithInputHandler
var WithToolResultStore = agent.WithToolResultStore

// WithEmbedding sets the shared embedding provider used by memory features
// (WithUserMemory + history.CrossThreadSearch). See agent.WithEmbedding.
var WithEmbedding = agent.WithEmbedding

// WithUserMemory enables the user-memory pipeline. Requires WithEmbedding.
// See agent.WithUserMemory.
var WithUserMemory = agent.WithUserMemory

var NewInMemoryToolResultStore = core.NewInMemoryToolResultStore
var WithToolResultMaxBytes = core.WithToolResultMaxBytes
var WithToolResultTTL = core.WithToolResultTTL
var ErrToolResultNotFound = core.ErrToolResultNotFound

// ToolMiddleware wraps an AnyTool with additional behavior. See
// core.ToolMiddleware.
type ToolMiddleware = core.ToolMiddleware

// WithToolMiddleware registers a chain of tool middlewares. See
// agent.WithToolMiddleware for ordering and rationale.
func WithToolMiddleware(mws ...ToolMiddleware) AgentOption {
	return agent.WithToolMiddleware(mws...)
}

// LoggingMiddleware logs tool start/finish events at slog.Info.
func LoggingMiddleware(logger *slog.Logger) ToolMiddleware {
	return agent.LoggingMiddleware(logger)
}

// TimingMiddleware logs tool duration at slog.Debug.
func TimingMiddleware() ToolMiddleware {
	return agent.TimingMiddleware()
}

// OTelSpanMiddleware emits a tool.execute span per call. Auto-wired when
// a Tracer is configured.
func OTelSpanMiddleware(tracer Tracer) ToolMiddleware {
	return agent.OTelSpanMiddleware(tracer)
}

// TransformMiddleware applies fn to the ToolResult before it returns to the LLM.
func TransformMiddleware(fn func(name string, r ToolResult) ToolResult) ToolMiddleware {
	return agent.TransformMiddleware(fn)
}

// WithToolApproval requires explicit human approval before the named tool
// runs. Composes with the InputHandler from WithInputHandler.
func WithToolApproval(toolName string, opts ...ApprovalOption) AgentOption {
	return agent.WithToolApproval(toolName, opts...)
}

// ApprovalOption is a functional option for WithToolApproval.
type ApprovalOption = agent.ApprovalOption

// ApprovalPrompt sets a custom prompt builder for an approval gate.
func ApprovalPrompt(fn func(call ToolCall) string) ApprovalOption {
	return agent.ApprovalPrompt(fn)
}

// OnDeny sets the action taken when a human denies an approval request.
func OnDeny(action DenyAction) ApprovalOption {
	return agent.OnDeny(action)
}

// DenyAction controls behavior when a human denies a tool approval request.
type DenyAction = agent.DenyAction

const (
	// DenyAskLLMToRevise returns an error result so the LLM can adapt.
	DenyAskLLMToRevise = agent.DenyAskLLMToRevise
	// DenyHalt halts the agent loop with *core.ErrHalt.
	DenyHalt = agent.DenyHalt
)

// TextResult wraps a plain string as a ToolResult. Use for hand-rolled tools producing plain text.
var TextResult = core.TextResult

// TextContent wraps a plain string as a JSON-quoted RawMessage for ToolResult.Content.
var TextContent = core.TextContent

// JSONContent wraps already-encoded JSON bytes as a ToolResult Content value.
var JSONContent = core.JSONContent

// --- Hooks and per-call overrides ---

// RunOptions overrides agent-level defaults for a single Execute call.
// See agent.RunOptions for full documentation.
type RunOptions = agent.RunOptions

// RunOptionsError reports a RunOptions validation failure.
type RunOptionsError = agent.RunOptionsError

// PrepareStep runs before each LLM call in the agent loop.
type PrepareStep = agent.PrepareStep

// OnIterationComplete runs after each loop iteration completes.
type OnIterationComplete = agent.OnIterationComplete

// OnError runs on mid-loop errors for recovery decisions.
type OnError = agent.OnError

// StepControl is the mutable control surface for PrepareStep hooks.
type StepControl = agent.StepControl

// IterationSnapshot is the read-only view passed to OnIterationComplete.
type IterationSnapshot = agent.IterationSnapshot

// IterationDecision is the return value of OnIterationComplete.
type IterationDecision = agent.IterationDecision

// ErrorDecision is the return value of OnError.
type ErrorDecision = agent.ErrorDecision

// AgentWithOptions extends Agent with ExecuteWith for per-call overrides.
type AgentWithOptions = agent.AgentWithOptions

// StreamingAgentWithOptions extends StreamingAgent with ExecuteStreamWith.
type StreamingAgentWithOptions = agent.StreamingAgentWithOptions

// Continue is the default IterationDecision — proceed to next iteration.
var Continue = agent.Continue

// Stop ends the agent run with the given result.
var Stop = agent.Stop

// InjectFeedback appends a user-role message and continues the loop.
var InjectFeedback = agent.InjectFeedback

// InjectMessages appends raw messages and continues the loop.
var InjectMessages = agent.InjectMessages

// Propagate bubbles the original error up.
var Propagate = agent.Propagate

// Retry re-runs the same iteration.
var Retry = agent.Retry

// RetryWithFeedback appends a message and retries the iteration.
var RetryWithFeedback = agent.RetryWithFeedback

// HaltDecision ends the run gracefully with a result and no error.
var HaltDecision = agent.HaltDecision

// WithPrepareStep registers a PrepareStep hook.
var WithPrepareStep = agent.WithPrepareStep

// WithOnIterationComplete registers an OnIterationComplete hook.
var WithOnIterationComplete = agent.WithOnIterationComplete

// WithOnError registers an OnError hook.
var WithOnError = agent.WithOnError

// WithMetadata adds static metadata to the agent.
var WithMetadata = agent.WithMetadata

// --- History ---

// WithHistory enables conversation history and related context-window management.
// Pass history.Option values from github.com/nevindra/oasis/history:
//
//	oasis.WithHistory(
//	    history.Store(store),
//	    history.MaxHistory(30),
//	    history.CrossThreadSearch(),
//	    history.Compaction(compactor, 0.8),
//	    history.Compress(model, 200_000),
//	)
var WithHistory = agent.WithHistory

// HistoryOption is an option for WithHistory. Import from github.com/nevindra/oasis/history.
type HistoryOption = history.Option

// --- Generation ---

// Generation groups LLM sampling and output parameters. Pass to WithGeneration.
// Pointer fields are optional — nil means "use provider default".
//
//	oasis.WithGeneration(oasis.Generation{
//	    Temperature: oasis.Ptr(0.5),
//	    TopP:        oasis.Ptr(0.9),
//	    TopK:        oasis.Ptr(40),
//	    MaxTokens:   oasis.Ptr(1024),
//	})
type Generation = agent.Generation

// WithGeneration sets LLM sampling and output parameters in one call.
var WithGeneration = agent.WithGeneration

// --- Helpers ---

// Ptr returns a pointer to v. Convenience for optional fields in Generation:
//
//	oasis.WithGeneration(oasis.Generation{Temperature: oasis.Ptr(0.5)})
func Ptr[T any](v T) *T { return &v }

// ModelFunc resolves the LLM provider per-request. Re-exported from core.
type ModelFunc = core.ModelFunc

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

// IterOver makes a step iterate over the collection at the given context key.
var IterOver = workflow.IterOver

// WithOnFinish registers a workflow-level completion callback.
var WithOnFinish = workflow.WithOnFinish

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
//		oasis.WithEmbedding(embedding),
//		oasis.WithUserMemory(memoryStore),
//		oasis.WithHistory(history.Store(store), history.CrossThreadSearch()),
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

// Chat is a non-streaming convenience wrapper around Provider.ChatStream.
// It discards stream events and returns the final assembled response.
// For UI-facing streaming, call ChatStream directly.
func Chat(ctx context.Context, p core.Provider, req core.ChatRequest) (core.ChatResponse, error) {
	return core.Chat(ctx, p, req)
}

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

// ToolMeta is the metadata an author writes for a Tool[In, Out] (name +
// description). The input schema is derived from In by reflection inside
// Erase — authors don't write JSON Schema by hand.
type ToolMeta = core.ToolMeta

// SchemaProvider is the opt-out for the reflection-based schema derivation
// performed by Erase. Input types may implement SchemaProvider to supply
// their own JSON Schema when reflection cannot express what the tool needs.
type SchemaProvider = core.SchemaProvider

// DeriveSchema returns the JSON Schema for T computed by reflection. Use
// this when you build a ToolDefinition by hand (built-in tools that don't
// go through Erase). Authors of normal Tool[In, Out] implementations do not
// need to call this — Erase does it.
func DeriveSchema[T any]() json.RawMessage { return core.DeriveSchema[T]() }

// NewToolRegistry creates an empty registry. See core.NewToolRegistry.
func NewToolRegistry() *ToolRegistry { return core.NewToolRegistry() }

// Erase converts a Tool[In, Out] into AnyTool. Forwards to core.Erase.
func Erase[In, Out any](t core.Tool[In, Out]) core.AnyTool {
	return core.Erase(t)
}

// ToolResultStore is the optional capability for paging large tool results.
type ToolResultStore = core.ToolResultStore

// StreamingTool re-exports core.StreamingTool for type-safe streaming tools.
type StreamingTool[In, Out any] = core.StreamingTool[In, Out]

// EraseStreaming converts a StreamingTool[In, Out] into a StreamingAnyTool.
// Forwards to core.EraseStreaming.
func EraseStreaming[In, Out any](t core.StreamingTool[In, Out]) core.StreamingAnyTool {
	return core.EraseStreaming(t)
}

// --- Tool robustness primitives ---

// ToolPolicy describes a per-tool timeout and retry policy applied by the
// agent's dispatch wrapper. See agent.WithToolPolicy / agent.WithToolPolicyMatch.
type ToolPolicy = core.ToolPolicy

// Retryable is the opt-in convention for marking a Go error as retryable.
// Use core.RetryableError to wrap an existing error so it satisfies this
// interface; DefaultRetryOn honors the wrapper via errors.As.
type Retryable = core.Retryable

// OutSchemaProvider is the opt-in override for Erase's auto-derived output
// schema. Tool implementations may implement this to publish a richer
// JSON Schema than reflection produces.
type OutSchemaProvider = core.OutSchemaProvider

// RetryableError wraps err so DefaultRetryOn reports it as retryable.
func RetryableError(err error) error { return core.RetryableError(err) }

// DefaultRetryOn is the predicate used when ToolPolicy.RetryOn is nil.
// Exported for composition: user predicates can fall through to it.
func DefaultRetryOn(err error) bool { return core.DefaultRetryOn(err) }

// --- LLM protocol types ---

type ChatMessage = core.ChatMessage

// Role is the originator of a chat message. See core.Role.
type Role = core.Role

const (
	RoleSystem    = core.RoleSystem
	RoleUser      = core.RoleUser
	RoleAssistant = core.RoleAssistant
	RoleTool      = core.RoleTool
)
type ChatRequest = core.ChatRequest
type ChatResponse = core.ChatResponse
type Attachment = core.Attachment

// NewAttachment constructs an Attachment from raw inline bytes.
func NewAttachment(mime string, data []byte) Attachment {
	return core.NewAttachment(mime, data)
}

// NewAttachmentFromURL constructs an Attachment from a remote URL.
func NewAttachmentFromURL(mime, url string) Attachment {
	return core.NewAttachmentFromURL(mime, url)
}

// NewAttachmentFromBase64 decodes a base64-encoded payload into an Attachment.
// Returns an error if the encoded string is not valid base64.
func NewAttachmentFromBase64(mime, encoded string) (Attachment, error) {
	return core.NewAttachmentFromBase64(mime, encoded)
}
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
	EventInputReceived      = core.EventInputReceived
	EventProcessingStart    = core.EventProcessingStart
	EventTextDelta          = core.EventTextDelta
	EventToolCallStart      = core.EventToolCallStart
	EventToolCallResult     = core.EventToolCallResult
	EventThinking           = core.EventThinking
	EventAgentStart         = core.EventAgentStart
	EventAgentFinish        = core.EventAgentFinish
	EventToolCallDelta      = core.EventToolCallDelta
	EventToolCallSuspended  = core.EventToolCallSuspended
	EventToolProgress       = core.EventToolProgress
	EventStepStart          = core.EventStepStart
	EventStepFinish         = core.EventStepFinish
	EventStepProgress       = core.EventStepProgress
	EventStepSuspended      = core.EventStepSuspended
	EventRoutingDecision    = core.EventRoutingDecision
	EventFileAttachment     = core.EventFileAttachment
	EventProcessorSuspended = core.EventProcessorSuspended

	// Lifecycle envelope (Phase 2 streaming).
	EventRunStart        = core.EventRunStart
	EventRunFinish       = core.EventRunFinish
	EventIterationStart  = core.EventIterationStart
	EventIterationFinish = core.EventIterationFinish
	// Structured object streaming (Phase 6 streaming).
	EventObjectDelta  = core.EventObjectDelta
	EventObjectFinish = core.EventObjectFinish
	EventElementDelta = core.EventElementDelta
)

// FinishReason describes why an agent run ended.
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

// Source, Sourced, Warner are the citation and warning interfaces.
type Source = core.Source
type Sourced = core.Sourced
type Warner = core.Warner

// Iteration and LLM call trace types.
type IterationTrace = core.IterationTrace
type LLMCallTrace = core.LLMCallTrace
type ToolCallTrace = core.ToolCallTrace

// Typed structured-output adapters.
// StreamObjectAs decodes typed structured-output snapshots from a Stream.
// See agent.StreamObjectAs for details.
func StreamObjectAs[T any](s *Stream) <-chan T {
	return agent.StreamObjectAs[T](s)
}

// ResultObjectAs decodes AgentResult.Object into T.
// See agent.ResultObjectAs for details.
func ResultObjectAs[T any](r AgentResult) (T, error) {
	return agent.ResultObjectAs[T](r)
}

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

// --- Stream wrapper ---

// Stream is an opt-in wrapper around StreamingAgent.ExecuteStream that
// provides multi-reader fan-out, bounded replay, blocking accessors, and
// event-typed callbacks. See agent.Stream for full documentation.
type Stream = agent.Stream

// StartStream runs agent.ExecuteStream in a background goroutine and returns
// a Stream that consumers may subscribe to or query for the final result.
func StartStream(ctx context.Context, ag StreamingAgent, task AgentTask) *Stream {
	return agent.StartStream(ctx, ag, task)
}

// StartStreamWith is the RunOptions-aware constructor for Stream.
func StartStreamWith(ctx context.Context, ag StreamingAgentWithOptions, task AgentTask, opts *RunOptions) *Stream {
	return agent.StartStreamWith(ctx, ag, task, opts)
}

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
