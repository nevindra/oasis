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
	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/compaction"
	"github.com/nevindra/oasis/guardrail"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/network"
	"github.com/nevindra/oasis/ratelimit"
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
