package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FromDefinition creates an executable *Workflow from a WorkflowDefinition and
// a registry of named agents, tools, and condition functions. The definition is
// validated at construction time: unknown agent/tool names, missing edge targets,
// condition nodes without branches, and cycles all produce errors.
//
// The returned Workflow uses the same DAG execution engine as compile-time
// workflows built with NewWorkflow.
func FromDefinition(def WorkflowDefinition, reg DefinitionRegistry) (*Workflow, error) {
	if len(def.Nodes) == 0 {
		return nil, fmt.Errorf("workflow definition %q: no nodes", def.Name)
	}

	// Index nodes by ID for lookups.
	nodeByID := make(map[string]*NodeDefinition, len(def.Nodes))
	for i := range def.Nodes {
		n := &def.Nodes[i]
		if _, dup := nodeByID[n.ID]; dup {
			return nil, fmt.Errorf("workflow definition %q: duplicate node ID %q", def.Name, n.ID)
		}
		nodeByID[n.ID] = n
	}

	// Build edge index: target -> list of sources (dependencies).
	// Also validate that all edge targets exist.
	deps := make(map[string][]string) // nodeID -> its After() dependencies
	for _, e := range def.Edges {
		from, to := e[0], e[1]
		if _, ok := nodeByID[from]; !ok {
			return nil, fmt.Errorf("workflow definition %q: edge references unknown node %q", def.Name, from)
		}
		if _, ok := nodeByID[to]; !ok {
			return nil, fmt.Errorf("workflow definition %q: edge references unknown node %q", def.Name, to)
		}
		deps[to] = append(deps[to], from)
	}

	// Build When() conditions for condition branch targets.
	// Branch targets get a When that checks the condition result before executing.
	// If multiple conditions route to the same target, compose with OR.
	branchWhen := make(map[string]func(*WorkflowContext) bool)
	for _, n := range def.Nodes {
		if n.Type != NodeCondition {
			continue
		}
		resultKey := n.ID + resultSuffix
		for _, target := range n.TrueBranch {
			rk := resultKey
			newFn := func(wCtx *WorkflowContext) bool {
				v, ok := wCtx.Get(rk)
				return ok && v == "true"
			}
			if existing, ok := branchWhen[target]; ok {
				prev := existing
				branchWhen[target] = func(wCtx *WorkflowContext) bool {
					return prev(wCtx) || newFn(wCtx)
				}
			} else {
				branchWhen[target] = newFn
			}
		}
		for _, target := range n.FalseBranch {
			rk := resultKey
			newFn := func(wCtx *WorkflowContext) bool {
				v, ok := wCtx.Get(rk)
				return ok && v == "false"
			}
			if existing, ok := branchWhen[target]; ok {
				prev := existing
				branchWhen[target] = func(wCtx *WorkflowContext) bool {
					return prev(wCtx) || newFn(wCtx)
				}
			} else {
				branchWhen[target] = newFn
			}
		}
	}

	// Validate and generate WorkflowOptions.
	var opts []WorkflowOption
	for _, n := range def.Nodes {
		generated, err := nodeToWorkflowOptions(n, nodeByID, deps[n.ID], reg, branchWhen[n.ID])
		if err != nil {
			return nil, fmt.Errorf("workflow definition %q: %w", def.Name, err)
		}
		opts = append(opts, generated...)
	}

	return NewWorkflow(def.Name, def.Description, opts...)
}

// nodeToWorkflowOptions converts a single NodeDefinition into one or more
// WorkflowOption values. A tool node with template args generates two steps
// (arg resolver + tool call). All other node types generate one step.
func nodeToWorkflowOptions(n NodeDefinition, nodes map[string]*NodeDefinition, after []string, reg DefinitionRegistry, when func(*WorkflowContext) bool) ([]WorkflowOption, error) {
	switch n.Type {
	case NodeLLM:
		return buildLLMNode(n, after, reg, when)
	case NodeTool:
		return buildToolNode(n, after, reg, when)
	case NodeCondition:
		return buildConditionNode(n, nodes, after, reg)
	case NodeTemplate:
		return buildTemplateNode(n, after, when)
	default:
		return nil, fmt.Errorf("node %q: unknown type %q", n.ID, n.Type)
	}
}

// buildLLMNode generates an AgentStep for an LLM node.
func buildLLMNode(n NodeDefinition, after []string, reg DefinitionRegistry, when func(*WorkflowContext) bool) ([]WorkflowOption, error) {
	agent, ok := reg.Agents[n.Agent]
	if !ok {
		return nil, fmt.Errorf("node %q: agent %q not found in registry", n.ID, n.Agent)
	}

	var stepOpts []StepOption
	if len(after) > 0 {
		stepOpts = append(stepOpts, After(after...))
	}
	if n.OutputTo != "" {
		stepOpts = append(stepOpts, OutputTo(n.OutputTo))
	}
	if n.Retry > 0 {
		stepOpts = append(stepOpts, Retry(n.Retry, time.Second))
	}
	if when != nil {
		stepOpts = append(stepOpts, When(when))
	}

	// If input has templates, use a custom Step that resolves them.
	if n.Input != "" && strings.Contains(n.Input, "{{") {
		return []WorkflowOption{
			Step(n.ID, func(ctx context.Context, wCtx *WorkflowContext) error {
				resolved := wCtx.Resolve(n.Input)
				result, err := agent.Execute(ctx, AgentTask{Input: resolved})
				if err != nil {
					return err
				}
				outputKey := n.ID + outputSuffix
				if n.OutputTo != "" {
					outputKey = n.OutputTo
				}
				wCtx.Set(outputKey, result.Output)
				wCtx.addUsage(result.Usage)
				return nil
			}, stepOpts...),
		}, nil
	}

	// No templates — use standard AgentStep with InputFrom if set.
	if n.Input != "" {
		stepOpts = append(stepOpts, InputFrom(n.Input))
	}
	return []WorkflowOption{AgentStep(n.ID, agent, stepOpts...)}, nil
}

// buildToolNode generates step(s) for a Tool node. If args contain templates,
// a preceding resolver step is generated.
func buildToolNode(n NodeDefinition, after []string, reg DefinitionRegistry, when func(*WorkflowContext) bool) ([]WorkflowOption, error) {
	tool, ok := reg.Tools[n.Tool]
	if !ok {
		return nil, fmt.Errorf("node %q: tool %q not found in registry", n.ID, n.Tool)
	}

	toolName := n.ToolName
	if toolName == "" {
		toolName = n.Tool
	}

	hasTemplates := false
	for _, v := range n.Args {
		if s, ok := v.(string); ok && strings.Contains(s, "{{") {
			hasTemplates = true
			break
		}
	}

	baseOpts := toolNodeBaseOpts(n, when)

	if hasTemplates {
		return buildToolNodeTemplateArgs(n, tool, toolName, after, when, baseOpts)
	}
	if len(n.Args) > 0 {
		return buildToolNodeStaticArgs(n, tool, toolName, after, baseOpts)
	}

	// No args — direct tool call.
	if len(after) > 0 {
		baseOpts = append(baseOpts, After(after...))
	}
	return []WorkflowOption{ToolStep(n.ID, tool, toolName, baseOpts...)}, nil
}

// toolNodeBaseOpts builds the common StepOption set shared by all tool node variants.
func toolNodeBaseOpts(n NodeDefinition, when func(*WorkflowContext) bool) []StepOption {
	var opts []StepOption
	if n.OutputTo != "" {
		opts = append(opts, OutputTo(n.OutputTo))
	}
	if n.Retry > 0 {
		opts = append(opts, Retry(n.Retry, time.Second))
	}
	if when != nil {
		opts = append(opts, When(when))
	}
	return opts
}

// buildToolNodeTemplateArgs generates a resolver step (resolves {{}} placeholders)
// followed by the tool call step.
func buildToolNodeTemplateArgs(n NodeDefinition, tool Tool, toolName string, after []string, when func(*WorkflowContext) bool, baseOpts []StepOption) ([]WorkflowOption, error) {
	resolverID := n.ID + argResolverSuffix
	var resolverAfter []StepOption
	if len(after) > 0 {
		resolverAfter = append(resolverAfter, After(after...))
	}
	if when != nil {
		resolverAfter = append(resolverAfter, When(when))
	}

	resolver := Step(resolverID, func(_ context.Context, wCtx *WorkflowContext) error {
		resolved := make(map[string]any, len(n.Args))
		for k, v := range n.Args {
			if s, ok := v.(string); ok && strings.Contains(s, "{{") {
				resolved[k] = wCtx.Resolve(s)
			} else {
				resolved[k] = v
			}
		}
		b, err := json.Marshal(resolved)
		if err != nil {
			return fmt.Errorf("node %s: marshal resolved args: %w", n.ID, err)
		}
		wCtx.Set(resolverID, json.RawMessage(b))
		return nil
	}, resolverAfter...)

	toolOpts := make([]StepOption, len(baseOpts), len(baseOpts)+2)
	copy(toolOpts, baseOpts)
	toolOpts = append(toolOpts, After(resolverID), ArgsFrom(resolverID))

	return []WorkflowOption{resolver, ToolStep(n.ID, tool, toolName, toolOpts...)}, nil
}

// buildToolNodeStaticArgs generates an arg-setter step (marshals static args)
// followed by the tool call step.
func buildToolNodeStaticArgs(n NodeDefinition, tool Tool, toolName string, after []string, baseOpts []StepOption) ([]WorkflowOption, error) {
	argsKey := n.ID + argResolverSuffix

	// The arg-setter inherits After + base opts so it runs at the right time.
	setterOpts := make([]StepOption, len(baseOpts))
	copy(setterOpts, baseOpts)
	if len(after) > 0 {
		setterOpts = append(setterOpts, After(after...))
	}

	setter := Step(argsKey, func(_ context.Context, wCtx *WorkflowContext) error {
		b, err := json.Marshal(n.Args)
		if err != nil {
			return fmt.Errorf("node %s: marshal args: %w", n.ID, err)
		}
		wCtx.Set(argsKey, json.RawMessage(b))
		return nil
	}, setterOpts...)

	// The tool step runs after the setter (which already gates on When),
	// so only After + ArgsFrom + OutputTo/Retry are needed here.
	toolOpts := []StepOption{After(argsKey), ArgsFrom(argsKey)}
	if n.OutputTo != "" {
		toolOpts = append(toolOpts, OutputTo(n.OutputTo))
	}
	if n.Retry > 0 {
		toolOpts = append(toolOpts, Retry(n.Retry, time.Second))
	}

	return []WorkflowOption{setter, ToolStep(n.ID, tool, toolName, toolOpts...)}, nil
}

// buildConditionNode generates a Step that evaluates the condition expression
// and writes "true" or "false" to context. Branch targets receive When()
// conditions via the branchWhen map built in FromDefinition.
func buildConditionNode(n NodeDefinition, nodes map[string]*NodeDefinition, after []string, reg DefinitionRegistry) ([]WorkflowOption, error) {
	if len(n.TrueBranch) == 0 && len(n.FalseBranch) == 0 {
		return nil, fmt.Errorf("node %q: condition has no true_branch or false_branch", n.ID)
	}

	// Validate branch targets exist.
	for _, target := range n.TrueBranch {
		if _, ok := nodes[target]; !ok {
			return nil, fmt.Errorf("node %q: true_branch references unknown node %q", n.ID, target)
		}
	}
	for _, target := range n.FalseBranch {
		if _, ok := nodes[target]; !ok {
			return nil, fmt.Errorf("node %q: false_branch references unknown node %q", n.ID, target)
		}
	}

	// Build the condition step.
	var stepOpts []StepOption
	if len(after) > 0 {
		stepOpts = append(stepOpts, After(after...))
	}

	resultKey := n.ID + resultSuffix
	expr := n.Expression

	condStep := Step(n.ID, func(_ context.Context, wCtx *WorkflowContext) error {
		// Check registered condition functions first.
		if fn, ok := reg.Conditions[expr]; ok {
			wCtx.Set(resultKey, strconv.FormatBool(fn(wCtx)))
			return nil
		}

		result, err := evalExpression(expr, wCtx)
		if err != nil {
			return fmt.Errorf("node %s: %w", n.ID, err)
		}
		wCtx.Set(resultKey, strconv.FormatBool(result))
		return nil
	}, stepOpts...)

	return []WorkflowOption{condStep}, nil
}

// buildTemplateNode generates a Step that resolves a template string.
func buildTemplateNode(n NodeDefinition, after []string, when func(*WorkflowContext) bool) ([]WorkflowOption, error) {
	if n.Template == "" {
		return nil, fmt.Errorf("node %q: template node has empty template", n.ID)
	}

	var stepOpts []StepOption
	if len(after) > 0 {
		stepOpts = append(stepOpts, After(after...))
	}
	if when != nil {
		stepOpts = append(stepOpts, When(when))
	}

	outputKey := n.ID + outputSuffix
	if n.OutputTo != "" {
		outputKey = n.OutputTo
		stepOpts = append(stepOpts, OutputTo(n.OutputTo))
	}
	tmpl := n.Template

	return []WorkflowOption{
		Step(n.ID, func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set(outputKey, wCtx.Resolve(tmpl))
			return nil
		}, stepOpts...),
	}, nil
}

// --- Expression evaluator ---

// expressionOperators lists comparison operators in parsing precedence order.
// Longer operators (>=, <=, !=, ==) are checked before shorter ones (>, <).
var expressionOperators = []string{"!=", "==", ">=", "<=", ">", "<", "contains"}

// evalExpression evaluates a simple comparison expression against resolved values
// from the WorkflowContext. Template placeholders ({{key}}) are resolved before
// evaluation.
//
// Supported operators: ==, !=, >, <, >=, <=, contains.
// Numeric comparison is attempted first; falls back to string comparison.
// The "contains" operator is always string-based.
//
// Security: the operator is located in the raw expression (before placeholder
// resolution) to prevent resolved values from injecting operators. Each side
// of the expression is then resolved and compared independently. Expression
// strings should come from workflow definitions, not from untrusted input.
func evalExpression(expr string, wCtx *WorkflowContext) (bool, error) {
	// Find the operator as a space-bounded token in the raw expression
	// (before resolving placeholders) to avoid matching operators inside
	// resolved values or literal substrings (e.g. "not-equal" containing "!=").
	for _, op := range expressionOperators {
		padded := " " + op + " "
		before, after, found := strings.Cut(expr, padded)
		if !found {
			continue
		}

		left := strings.TrimSpace(wCtx.Resolve(before))
		right := strings.TrimSpace(wCtx.Resolve(after))
		left = stripQuotes(left)
		right = stripQuotes(right)

		return evalCompare(left, right, op)
	}

	return false, fmt.Errorf("expression: no operator found in %q (operators must be space-bounded, e.g. \"x == y\")", expr)
}

// evalCompare performs the comparison between left and right using the given operator.
func evalCompare(left, right, op string) (bool, error) {
	if op == "contains" {
		return strings.Contains(left, right), nil
	}

	// Try numeric comparison.
	lf, lErr := strconv.ParseFloat(left, 64)
	rf, rErr := strconv.ParseFloat(right, 64)
	if lErr == nil && rErr == nil {
		switch op {
		case "==":
			return lf == rf, nil
		case "!=":
			return lf != rf, nil
		case ">":
			return lf > rf, nil
		case "<":
			return lf < rf, nil
		case ">=":
			return lf >= rf, nil
		case "<=":
			return lf <= rf, nil
		}
	}

	// Fall back to string comparison.
	switch op {
	case "==":
		return left == right, nil
	case "!=":
		return left != right, nil
	case ">":
		return left > right, nil
	case "<":
		return left < right, nil
	case ">=":
		return left >= right, nil
	case "<=":
		return left <= right, nil
	default:
		return false, fmt.Errorf("expression: unsupported operator %q", op)
	}
}

// stripQuotes removes surrounding single or double quotes from a string literal.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
