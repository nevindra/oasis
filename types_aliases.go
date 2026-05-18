package oasis

import (
	"time"

	"github.com/nevindra/oasis/core"
)

// Temporary aliases during Phase 0 migration. These keep existing root-package
// callers (and the current satellite modules) compiling without rewriting every
// reference site. Phase 2+ moves callers into subpackages that import `core`
// directly, at which point this file is deleted.
//
// New code should prefer importing github.com/nevindra/oasis/core directly.

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
