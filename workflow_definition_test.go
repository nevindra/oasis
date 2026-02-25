package oasis

import (
	"context"
	"encoding/json"
	"testing"
)

// --- Expression evaluator tests ---

func TestEvalExpression(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		values  map[string]any
		want    bool
		wantErr bool
	}{
		{"string equal", "{{status}} == 'active'", map[string]any{"status": "active"}, true, false},
		{"string not equal", "{{status}} != 'active'", map[string]any{"status": "inactive"}, true, false},
		{"string equal false", "{{status}} == 'active'", map[string]any{"status": "inactive"}, false, false},
		{"numeric greater", "{{score}} > 0.5", map[string]any{"score": 0.8}, true, false},
		{"numeric less", "{{score}} < 0.5", map[string]any{"score": 0.3}, true, false},
		{"numeric equal", "{{score}} == 1", map[string]any{"score": 1.0}, true, false},
		{"numeric gte", "{{score}} >= 0.5", map[string]any{"score": 0.5}, true, false},
		{"numeric lte", "{{score}} <= 0.5", map[string]any{"score": 0.5}, true, false},
		{"contains true", "{{text}} contains 'urgent'", map[string]any{"text": "this is urgent"}, true, false},
		{"contains false", "{{text}} contains 'urgent'", map[string]any{"text": "this is normal"}, false, false},
		{"empty string check", "{{result}} != ''", map[string]any{"result": "data"}, true, false},
		{"empty string empty", "{{result}} != ''", map[string]any{"result": ""}, false, false},
		{"no operator", "just a string", nil, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wCtx := newWorkflowContext(AgentTask{})
			for k, v := range tt.values {
				wCtx.Set(k, v)
			}
			got, err := evalExpression(tt.expr, wCtx)
			if (err != nil) != tt.wantErr {
				t.Fatalf("evalExpression(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("evalExpression(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

// --- FromDefinition tests ---

func TestFromDefinitionLLMNode(t *testing.T) {
	agent := &stubAgent{
		name: "writer",
		desc: "Writes text",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: "wrote: " + task.Input}, nil
		},
	}

	def := WorkflowDefinition{
		Name:        "llm-test",
		Description: "LLM node test",
		Nodes: []NodeDefinition{
			{ID: "write", Type: NodeLLM, Agent: "writer", Input: "Summarize: {{input}}"},
		},
		Edges: [][2]string{},
	}

	reg := DefinitionRegistry{
		Agents: map[string]Agent{"writer": agent},
	}

	wf, err := FromDefinition(def, reg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "wrote: Summarize: hello" {
		t.Errorf("Output = %q, want %q", result.Output, "wrote: Summarize: hello")
	}
}

func TestFromDefinitionToolNode(t *testing.T) {
	tool := mockTool{} // returns "hello from <name>"

	def := WorkflowDefinition{
		Name:        "tool-test",
		Description: "Tool node test",
		Nodes: []NodeDefinition{
			{ID: "greet", Type: NodeTool, Tool: "greeter", ToolName: "greet"},
		},
		Edges: [][2]string{},
	}

	reg := DefinitionRegistry{
		Tools: map[string]Tool{"greeter": tool},
	}

	wf, err := FromDefinition(def, reg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "hello from greet" {
		t.Errorf("Output = %q, want %q", result.Output, "hello from greet")
	}
}

func TestFromDefinitionTemplateNode(t *testing.T) {
	def := WorkflowDefinition{
		Name:        "tmpl-test",
		Description: "Template node test",
		Nodes: []NodeDefinition{
			{ID: "set", Type: NodeTemplate, Template: "no templates here"},
			{ID: "fmt", Type: NodeTemplate, Template: "Result: {{set.output}}", OutputTo: "final"},
		},
		Edges: [][2]string{{"set", "fmt"}},
	}

	wf, err := FromDefinition(def, DefinitionRegistry{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "Result: no templates here" {
		t.Errorf("Output = %q, want %q", result.Output, "Result: no templates here")
	}
}

func TestFromDefinitionConditionBranching(t *testing.T) {
	def := WorkflowDefinition{
		Name:        "cond-test",
		Description: "Condition branching test",
		Nodes: []NodeDefinition{
			{ID: "setup", Type: NodeTemplate, Template: "data"},
			{ID: "check", Type: NodeCondition,
				Expression:  "{{setup.output}} == 'data'",
				TrueBranch:  []string{"yes"},
				FalseBranch: []string{"no"},
			},
			{ID: "yes", Type: NodeTemplate, Template: "took true branch"},
			{ID: "no", Type: NodeTemplate, Template: "took false branch"},
		},
		Edges: [][2]string{{"setup", "check"}, {"check", "yes"}, {"check", "no"}},
	}

	wf, err := FromDefinition(def, DefinitionRegistry{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "took true branch" {
		t.Errorf("Output = %q, want %q", result.Output, "took true branch")
	}
}

func TestFromDefinitionConditionFalseBranch(t *testing.T) {
	def := WorkflowDefinition{
		Name:        "cond-false",
		Description: "Condition false branch test",
		Nodes: []NodeDefinition{
			{ID: "setup", Type: NodeTemplate, Template: "other"},
			{ID: "check", Type: NodeCondition,
				Expression:  "{{setup.output}} == 'data'",
				TrueBranch:  []string{"yes"},
				FalseBranch: []string{"no"},
			},
			{ID: "yes", Type: NodeTemplate, Template: "took true branch"},
			{ID: "no", Type: NodeTemplate, Template: "took false branch"},
		},
		Edges: [][2]string{{"setup", "check"}, {"check", "yes"}, {"check", "no"}},
	}

	wf, err := FromDefinition(def, DefinitionRegistry{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "took false branch" {
		t.Errorf("Output = %q, want %q", result.Output, "took false branch")
	}
}

func TestFromDefinitionRegisteredCondition(t *testing.T) {
	def := WorkflowDefinition{
		Name:        "cond-func",
		Description: "Registered condition function test",
		Nodes: []NodeDefinition{
			{ID: "check", Type: NodeCondition,
				Expression:  "always_true",
				TrueBranch:  []string{"yes"},
				FalseBranch: []string{"no"},
			},
			{ID: "yes", Type: NodeTemplate, Template: "true path"},
			{ID: "no", Type: NodeTemplate, Template: "false path"},
		},
		Edges: [][2]string{{"check", "yes"}, {"check", "no"}},
	}

	reg := DefinitionRegistry{
		Conditions: map[string]func(*WorkflowContext) bool{
			"always_true": func(_ *WorkflowContext) bool { return true },
		},
	}

	wf, err := FromDefinition(def, reg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "true path" {
		t.Errorf("Output = %q, want %q", result.Output, "true path")
	}
}

// --- FromDefinition validation tests ---

func TestFromDefinitionValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		def  WorkflowDefinition
		reg  DefinitionRegistry
	}{
		{
			"no nodes",
			WorkflowDefinition{Name: "empty"},
			DefinitionRegistry{},
		},
		{
			"duplicate node ID",
			WorkflowDefinition{Name: "dup", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeTemplate, Template: "x"},
				{ID: "a", Type: NodeTemplate, Template: "y"},
			}},
			DefinitionRegistry{},
		},
		{
			"unknown edge target",
			WorkflowDefinition{Name: "bad-edge", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeTemplate, Template: "x"},
			}, Edges: [][2]string{{"a", "b"}}},
			DefinitionRegistry{},
		},
		{
			"unknown agent",
			WorkflowDefinition{Name: "bad-agent", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeLLM, Agent: "missing"},
			}},
			DefinitionRegistry{Agents: map[string]Agent{}},
		},
		{
			"unknown tool",
			WorkflowDefinition{Name: "bad-tool", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeTool, Tool: "missing"},
			}},
			DefinitionRegistry{Tools: map[string]Tool{}},
		},
		{
			"condition no branches",
			WorkflowDefinition{Name: "bad-cond", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeCondition, Expression: "{{x}} == 1"},
			}},
			DefinitionRegistry{},
		},
		{
			"unknown node type",
			WorkflowDefinition{Name: "bad-type", Nodes: []NodeDefinition{
				{ID: "a", Type: "invalid"},
			}},
			DefinitionRegistry{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromDefinition(tt.def, tt.reg)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestFromDefinitionToolWithTemplateArgs(t *testing.T) {
	// Tool that echoes its args as content.
	echoTool := &argEchoTool{}

	def := WorkflowDefinition{
		Name:        "tool-args",
		Description: "Tool with template args",
		Nodes: []NodeDefinition{
			{ID: "setup", Type: NodeTemplate, Template: "world"},
			{ID: "call", Type: NodeTool, Tool: "echo", ToolName: "echo_args",
				Args: map[string]any{"greeting": "hello {{setup.output}}"}},
		},
		Edges: [][2]string{{"setup", "call"}},
	}

	reg := DefinitionRegistry{
		Tools: map[string]Tool{"echo": echoTool},
	}

	wf, err := FromDefinition(def, reg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// The argEchoTool returns the raw JSON args as content.
	if result.Output == "" {
		t.Error("expected non-empty output from tool with template args")
	}
}

// argEchoTool is a test tool that returns its arguments as the result content.
type argEchoTool struct{}

func (a *argEchoTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "echo_args", Description: "Echoes args"}}
}

func (a *argEchoTool) Execute(_ context.Context, _ string, args json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: string(args)}, nil
}
