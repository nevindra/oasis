# Execution Plans

This guide shows how to compose existing Oasis primitives into an execution plan pattern — where an LLM proposes a plan, a human reviews it, and the framework executes it step-by-step.

No new APIs are needed. The pattern composes `Workflow`, `Suspend`/`Resume`, and optionally `FromDefinition`.

## When to Use This

Use execution plans when:

- **Destructive operations** — the agent wants to delete files, modify databases, or call external APIs with side effects
- **Multi-step pipelines** — the LLM identifies a sequence of actions and you want human sign-off before running them
- **Audit requirements** — you need a record of what the agent planned vs. what it executed

If the agent's actions are safe and reversible, let it act directly — execution plans add latency.

## Pattern 1: Static Plan with Approval Gate

When you know the pipeline shape but want human approval at a specific point:

```go
wf, _ := oasis.NewWorkflow("deploy", "Deploy with approval",
    oasis.AgentStep("analyze", analyzer),
    oasis.Step("approve", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
        if data, ok := oasis.ResumeData(wCtx); ok {
            var d struct{ Approved bool `json:"approved"` }
            json.Unmarshal(data, &d)
            if !d.Approved {
                return fmt.Errorf("deployment rejected by human")
            }
            return nil
        }
        analysis, _ := wCtx.Get("analyze.output")
        return oasis.Suspend(json.RawMessage(fmt.Sprintf(
            `{"action": "approve_deploy", "analysis": %q}`, analysis,
        )))
    }, oasis.After("analyze")),
    oasis.AgentStep("deploy", deployer, oasis.After("approve")),
)

result, err := wf.Execute(ctx, task)
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    // Show suspended.Payload to user, wait for decision...
    result, err = suspended.Resume(ctx, json.RawMessage(`{"approved": true}`))
}
```

## Pattern 2: LLM-Generated Plan

When the LLM decides what steps to run. Use a planner agent to generate a structured plan, then compile it into a Workflow with `FromDefinition`.

### Step 1: Planner Agent

```go
planner := oasis.NewLLMAgent("planner", "You are a task planner.", llm,
    oasis.WithResponseSchema(oasis.NewResponseSchema("plan", &oasis.SchemaObject{
        Type: "object",
        Properties: map[string]*oasis.SchemaObject{
            "steps": {
                Type: "array",
                Items: &oasis.SchemaObject{
                    Type: "object",
                    Properties: map[string]*oasis.SchemaObject{
                        "id":    {Type: "string"},
                        "tool":  {Type: "string"},
                        "func":  {Type: "string"},
                        "args":  {Type: "object"},
                        "after": {Type: "array", Items: &oasis.SchemaObject{Type: "string"}},
                    },
                    Required: []string{"id", "tool"},
                },
            },
        },
        Required: []string{"steps"},
    })),
)
```

### Step 2: Parse Plan into WorkflowDefinition

```go
type Plan struct {
    Steps []PlanStep `json:"steps"`
}
type PlanStep struct {
    ID    string         `json:"id"`
    Tool  string         `json:"tool"`
    Func  string         `json:"func"`
    Args  map[string]any `json:"args"`
    After []string       `json:"after"`
}

func planToDefinition(plan Plan) oasis.WorkflowDefinition {
    def := oasis.WorkflowDefinition{
        Name:        "llm-plan",
        Description: "LLM-generated execution plan",
    }
    for _, s := range plan.Steps {
        def.Nodes = append(def.Nodes, oasis.NodeDefinition{
            ID:       s.ID,
            Type:     oasis.NodeTool,
            Tool:     s.Tool,
            ToolName: s.Func,
            Args:     s.Args,
        })
        for _, dep := range s.After {
            def.Edges = append(def.Edges, [2]string{dep, s.ID})
        }
    }
    return def
}
```

### Step 3: Approval + Execution

```go
func executePlan(ctx context.Context, input string, planner oasis.Agent, reg oasis.DefinitionRegistry) (oasis.AgentResult, error) {
    // 1. LLM generates plan
    planResult, err := planner.Execute(ctx, oasis.AgentTask{Input: input})
    if err != nil {
        return oasis.AgentResult{}, fmt.Errorf("planning: %w", err)
    }

    var plan Plan
    json.Unmarshal([]byte(planResult.Output), &plan)

    // 2. Build workflow from plan
    def := planToDefinition(plan)
    wf, err := oasis.FromDefinition(def, reg)
    if err != nil {
        return oasis.AgentResult{}, fmt.Errorf("compiling plan: %w", err)
    }

    // 3. Execute
    return wf.Execute(ctx, oasis.AgentTask{Input: input})
}
```

### Adding Human Approval

Wrap the workflow in an outer workflow that suspends for approval:

```go
func executePlanWithApproval(ctx context.Context, input string, planner oasis.Agent, reg oasis.DefinitionRegistry) (oasis.AgentResult, error) {
    // 1. Generate plan
    planResult, err := planner.Execute(ctx, oasis.AgentTask{Input: input})
    if err != nil {
        return oasis.AgentResult{}, fmt.Errorf("planning: %w", err)
    }

    var plan Plan
    json.Unmarshal([]byte(planResult.Output), &plan)

    // 2. Compile plan into workflow
    def := planToDefinition(plan)
    inner, err := oasis.FromDefinition(def, reg)
    if err != nil {
        return oasis.AgentResult{}, fmt.Errorf("compiling plan: %w", err)
    }

    // 3. Wrap with approval gate
    outer, _ := oasis.NewWorkflow("plan-with-approval", "Executes LLM plan after human approval",
        oasis.Step("review", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
            if data, ok := oasis.ResumeData(wCtx); ok {
                var d struct{ Approved bool `json:"approved"` }
                json.Unmarshal(data, &d)
                if !d.Approved {
                    return fmt.Errorf("plan rejected")
                }
                return nil
            }
            // Send plan to human for review
            planJSON, _ := json.Marshal(plan)
            return oasis.Suspend(planJSON)
        }),
        oasis.AgentStep("execute", inner, oasis.After("review")),
    )

    return outer.Execute(ctx, oasis.AgentTask{Input: input})
}
```

The caller handles suspension as usual:

```go
result, err := executePlanWithApproval(ctx, "deploy the new API version", planner, reg)

var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    // suspended.Payload contains the plan JSON — show to user
    fmt.Println("Plan:", string(suspended.Payload))

    // After human reviews:
    result, err = suspended.Resume(ctx, json.RawMessage(`{"approved": true}`))
}
```

### Error Handling

When an inner workflow step fails, `wf.Execute` returns a `*WorkflowError` with details about which step failed:

```go
result, err := wf.Execute(ctx, oasis.AgentTask{Input: input})

var wfErr *oasis.WorkflowError
if errors.As(err, &wfErr) {
    fmt.Println("failed step:", wfErr.StepName)
    fmt.Println("cause:", wfErr.Err)
    for name, step := range wfErr.Result.Steps {
        fmt.Printf("  %s: %s\n", name, step.Status)
    }
}
```

Don't confuse `WorkflowError` with `ErrSuspended` — suspension is not a failure. Check for `ErrSuspended` first.

## Pattern 3: Plan Modification

The human might want to modify the plan before approving. Handle this by re-compiling:

```go
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    var plan Plan
    json.Unmarshal(suspended.Payload, &plan)

    // Human removes step 3, reorders steps, etc.
    plan.Steps = modifyPlan(plan.Steps)

    // Re-compile modified plan
    def := planToDefinition(plan)
    wf, _ := oasis.FromDefinition(def, reg)
    result, err = wf.Execute(ctx, oasis.AgentTask{Input: input})
}
```

This avoids a dedicated "plan store" — the plan is just data that flows through the Suspend/Resume boundary.

## Advanced: Condition and Template Nodes

Beyond `tool` and `llm` node types, `FromDefinition` supports `condition` (branching) and `template` (string interpolation) nodes for richer runtime workflows.

### Condition Branching

Route execution based on previous step results:

```go
def := oasis.WorkflowDefinition{
    Name: "conditional-pipeline",
    Nodes: []oasis.NodeDefinition{
        {ID: "analyze", Type: oasis.NodeTool, Tool: "analyzer", ToolName: "classify",
            Args: map[string]any{"text": "{{input}}"}},
        {ID: "check", Type: oasis.NodeCondition,
            Expression:  "{{analyze.result}} contains critical",
            TrueBranch:  []string{"escalate"},
            FalseBranch: []string{"auto-handle"},
        },
        {ID: "escalate", Type: oasis.NodeLLM, Agent: "senior",
            Input: "Critical issue: {{analyze.result}}"},
        {ID: "auto-handle", Type: oasis.NodeLLM, Agent: "junior",
            Input: "Handle routine issue: {{analyze.result}}"},
    },
    Edges: [][2]string{{"analyze", "check"}},
}
```

Supported operators: `==`, `!=`, `>`, `<`, `>=`, `<=`, `contains`. Operators must be space-bounded (e.g. `{{x}} == y`, not `{{x}}==y`). For complex logic, register a Go function:

```go
reg := oasis.DefinitionRegistry{
    Conditions: map[string]func(*oasis.WorkflowContext) bool{
        "needs_escalation": func(wCtx *oasis.WorkflowContext) bool {
            score, _ := wCtx.Get("analyze.result")
            // complex logic here
            return strings.Contains(score.(string), "critical")
        },
    },
}
// Reference by name: Expression: "needs_escalation"
```

### Template Nodes

Interpolate values from previous steps without calling an LLM:

```go
{ID: "format", Type: oasis.NodeTemplate,
    Input: "Analysis complete. Result: {{analyze.result}}. Action: {{handle.output}}"},
```

Template nodes use `{{key}}` placeholders that resolve against `WorkflowContext`. Missing keys resolve to empty strings.

## Tips

- **Keep plans serializable.** Use JSON-friendly structs so plans can cross process boundaries (webhooks, queues, databases).
- **Don't over-plan.** If the LLM can execute 2-3 tool calls safely, use `WithPlanExecution()` for parallel batching instead of the full plan pattern.
- **Validate plans.** `FromDefinition` validates at construction time (unknown tools, cycles, missing edges). Use this as a guard against malformed LLM output.
- **Combine with processors.** A `PreProcessor` can intercept dangerous tool calls and force the plan pattern automatically.

## See Also

- [Workflow](../concepts/workflow.md) — DAG execution, `FromDefinition`, step types
- [Human-in-the-Loop](human-in-the-loop.md) — `Suspend`/`Resume`, `InputHandler`
- [Background Agents](background-agents.md) — `Spawn` for fire-and-forget execution
- [Tool Concept](../concepts/tool.md) — `WithPlanExecution()` for parallel batching
