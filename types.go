package oasis

import "github.com/nevindra/oasis/workflow"

// This file re-exports workflow definition types from the workflow subpackage.
// The canonical definitions live in github.com/nevindra/oasis/workflow.

// --- Runtime workflow definition types ---

// NodeType re-exported from workflow.
type NodeType = workflow.NodeType

const (
	// NodeLLM delegates to a registered Agent.
	NodeLLM NodeType = workflow.NodeLLM
	// NodeTool calls a registered Tool function.
	NodeTool NodeType = workflow.NodeTool
	// NodeCondition evaluates an expression and routes to true/false branches.
	NodeCondition NodeType = workflow.NodeCondition
	// NodeTemplate performs string interpolation via WorkflowContext.Resolve.
	NodeTemplate NodeType = workflow.NodeTemplate
)

// WorkflowDefinition re-exported from workflow.
type WorkflowDefinition = workflow.WorkflowDefinition

// NodeDefinition re-exported from workflow.
type NodeDefinition = workflow.NodeDefinition

// DefinitionRegistry re-exported from workflow.
type DefinitionRegistry = workflow.DefinitionRegistry
