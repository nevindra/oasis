package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- Workflow context ---

// WorkflowContext is the shared state that flows between workflow steps.
// Steps read/write named keys to pass data between each other.
// All methods are safe for concurrent use.
type WorkflowContext struct {
	values map[string]any
	input  string
	task   AgentTask // original task (for propagating Context/Attachments to AgentSteps)
	mu     sync.RWMutex
}

// newWorkflowContext creates a WorkflowContext initialized with the original task.
func newWorkflowContext(task AgentTask) *WorkflowContext {
	v := make(map[string]any)
	if task.Input != "" {
		v["input"] = task.Input
	}
	return &WorkflowContext{
		values: v,
		input:  task.Input,
		task:   task,
	}
}

// Get retrieves a named value from the context.
// Returns the value and true if found, or nil and false if the key does not exist.
func (c *WorkflowContext) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.values[key]
	return v, ok
}

// Set writes a named value to the context, overwriting any previous value for that key.
func (c *WorkflowContext) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = value
}

// Input returns the original AgentTask.Input that started the workflow.
func (c *WorkflowContext) Input() string {
	return c.input
}

// addUsage atomically accumulates token usage from an AgentStep execution.
// Uses a reserved key ("_usage") in the values map. Safe for concurrent calls
// from parallel AgentSteps.
func (c *WorkflowContext) addUsage(u Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.values["_usage"]; ok {
		if eu, ok := existing.(Usage); ok {
			u.InputTokens += eu.InputTokens
			u.OutputTokens += eu.OutputTokens
		}
	}
	c.values["_usage"] = u
}

// Resolve replaces {{key}} placeholders in template with values from the
// context's values map. Unknown keys resolve to empty strings. Values are
// converted via fmt.Sprintf("%v", v). If the template contains no placeholders,
// it is returned as-is.
func (c *WorkflowContext) Resolve(template string) string {
	if !strings.Contains(template, "{{") {
		return template
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	var b strings.Builder
	s := template
	for {
		start := strings.Index(s, "{{")
		if start == -1 {
			b.WriteString(s)
			break
		}
		end := strings.Index(s[start:], "}}")
		if end == -1 {
			b.WriteString(s)
			break
		}
		end += start // adjust to absolute index

		b.WriteString(s[:start])
		key := strings.TrimSpace(s[start+2 : end])
		if v, ok := c.values[key]; ok {
			b.WriteString(fmt.Sprintf("%v", v))
		}
		s = s[end+2:]
	}
	return b.String()
}

// ResolveJSON is like Resolve but returns json.RawMessage. If the template is
// a single placeholder (e.g. "{{key}}") and the value is not a string, the
// value is marshalled to JSON directly (preserving structure). Otherwise it
// behaves like Resolve and wraps the result as a JSON string.
func (c *WorkflowContext) ResolveJSON(template string) json.RawMessage {
	trimmed := strings.TrimSpace(template)

	// Fast path: single placeholder — return value as JSON directly.
	if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") &&
		strings.Count(trimmed, "{{") == 1 {
		key := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
		c.mu.RLock()
		v, ok := c.values[key]
		c.mu.RUnlock()
		if ok {
			b, err := json.Marshal(v)
			if err == nil {
				return b
			}
		}
		return json.RawMessage(`null`)
	}

	// Multi-placeholder or mixed text: resolve as string.
	resolved := c.Resolve(template)
	b, err := json.Marshal(resolved)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}

// --- Step status ---

// StepStatus represents the execution state of a workflow step.
type StepStatus string

const (
	// StepPending indicates a step that has not started execution.
	StepPending StepStatus = "pending"
	// StepRunning indicates a step that is currently executing.
	StepRunning StepStatus = "running"
	// StepSuccess indicates a step that completed without error.
	StepSuccess StepStatus = "success"
	// StepSkipped indicates a step that was not executed because its
	// When() condition returned false or an upstream step failed.
	StepSkipped StepStatus = "skipped"
	// StepFailed indicates a step that returned an error after exhausting retries.
	StepFailed StepStatus = "failed"
)

// --- Step and workflow results ---

// StepResult holds the outcome of a single step execution.
type StepResult struct {
	// Name is the step's unique identifier within the workflow.
	Name string
	// Status is the final execution state of the step.
	Status StepStatus
	// Output is the step's output string (from context), empty if the step failed or was skipped.
	Output string
	// Error is the error that caused the step to fail, nil on success or skip.
	Error error
	// Duration is the wall-clock time the step took to execute, including retries.
	Duration time.Duration
}

// WorkflowResult is the aggregate outcome of a full workflow execution.
// Returned via the onFinish callback; also used to build the AgentResult.
type WorkflowResult struct {
	// Status is StepSuccess if all steps succeeded (or were skipped by condition),
	// StepFailed if any step failed.
	Status StepStatus
	// Steps maps step name to its individual result.
	Steps map[string]StepResult
	// Context is the shared WorkflowContext after all steps have run.
	// Callers can inspect final values set by steps.
	Context *WorkflowContext
	// Usage is the aggregate token usage from all AgentStep executions.
	Usage Usage
}

// WorkflowError is returned by Workflow.Execute when one or more steps fail.
// Callers can inspect per-step results via errors.As:
//
//	result, err := wf.Execute(ctx, task)
//	var wfErr *WorkflowError
//	if errors.As(err, &wfErr) {
//	    for name, step := range wfErr.Result.Steps { ... }
//	}
type WorkflowError struct {
	// StepName is the name of the first step that failed.
	StepName string
	// Err is the underlying error from the failed step.
	Err error
	// Result is the full workflow result with per-step outcomes.
	Result WorkflowResult
}

func (e *WorkflowError) Error() string {
	return fmt.Sprintf("workflow step %q failed: %v", e.StepName, e.Err)
}

func (e *WorkflowError) Unwrap() error {
	return e.Err
}

// --- Step function type ---

// StepFunc is the signature for custom function steps. The function receives
// the parent context (which is cancelled on workflow failure) and the shared
// WorkflowContext for reading/writing step data.
type StepFunc func(ctx context.Context, wCtx *WorkflowContext) error

// --- Internal config types ---

type stepType int

const (
	stepTypeBasic   stepType = iota
	stepTypeForEach          // iterates over a collection
	stepTypeDoUntil          // loops until condition is true
	stepTypeDoWhile          // loops while condition is true
)

// stepConfig holds the full configuration for a single workflow step.
type stepConfig struct {
	name      string
	fn        StepFunc
	after     []string                   // dependency edges
	when      func(*WorkflowContext) bool // conditional execution gate
	inputFrom string                      // AgentStep: context key for input
	argsFrom  string                      // ToolStep: context key for args
	outputTo  string                      // override default output key
	retry     int                         // max retry count (0 = no retries)
	retryDelay time.Duration              // delay between retries

	// ForEach fields
	iterOver    string // context key containing []any
	concurrency int    // max parallel iterations (default 1)

	// Loop fields
	until   func(*WorkflowContext) bool // DoUntil: exit when true
	whileFn func(*WorkflowContext) bool // DoWhile: continue while true
	maxIter int                         // loop safety cap (default 10)

	stepType stepType
}

// workflowConfig accumulates options passed to NewWorkflow.
type workflowConfig struct {
	steps        []*stepConfig
	onFinish     func(WorkflowResult)
	onError      func(string, error)
	defaultRetry int
	defaultDelay time.Duration
}

// --- Step options ---

// StepOption configures an individual workflow step.
type StepOption func(*stepConfig)

// After declares dependency edges: this step runs only after all named steps
// have completed successfully (or been skipped by condition).
func After(steps ...string) StepOption {
	return func(c *stepConfig) { c.after = append(c.after, steps...) }
}

// When sets a condition function that is evaluated before the step runs.
// If the function returns false, the step is marked StepSkipped and its
// dependents treat it as satisfied.
func When(fn func(*WorkflowContext) bool) StepOption {
	return func(c *stepConfig) { c.when = fn }
}

// InputFrom sets the context key whose value becomes the AgentTask.Input
// for an AgentStep. If not set, the original WorkflowContext.Input() is used.
func InputFrom(key string) StepOption {
	return func(c *stepConfig) { c.inputFrom = key }
}

// ArgsFrom sets the context key whose value becomes the tool arguments
// for a ToolStep. The value should be json.RawMessage, a JSON string,
// or any value that can be marshalled to JSON.
func ArgsFrom(key string) StepOption {
	return func(c *stepConfig) { c.argsFrom = key }
}

// OutputTo overrides the default output key for AgentStep ("{name}.output")
// or ToolStep ("{name}.result"). Has no effect on basic Step (which writes
// to context explicitly via wCtx.Set).
func OutputTo(key string) StepOption {
	return func(c *stepConfig) { c.outputTo = key }
}

// Retry configures the step to be retried up to n times on failure,
// with the given delay between attempts. The total attempts = 1 + n.
func Retry(n int, delay time.Duration) StepOption {
	return func(c *stepConfig) {
		c.retry = n
		c.retryDelay = delay
	}
}

// IterOver sets the context key that contains a []any collection for a
// ForEach step. Each element is made available to the step function via
// the context key "{name}.item".
func IterOver(key string) StepOption {
	return func(c *stepConfig) { c.iterOver = key }
}

// Concurrency sets the maximum number of parallel iterations for a ForEach step.
// Defaults to 1 (sequential) if not specified.
func Concurrency(n int) StepOption {
	return func(c *stepConfig) { c.concurrency = n }
}

// Until sets the exit condition for a DoUntil step. The step repeats until
// the function returns true (evaluated after each iteration).
func Until(fn func(*WorkflowContext) bool) StepOption {
	return func(c *stepConfig) { c.until = fn }
}

// MaxIter sets the safety cap on loop iterations for DoUntil and DoWhile steps.
// Defaults to 10 if not specified.
func MaxIter(n int) StepOption {
	return func(c *stepConfig) { c.maxIter = n }
}

// --- Workflow options ---

// WorkflowOption configures a Workflow. Step definitions (Step, AgentStep, ToolStep,
// ForEach, DoUntil, DoWhile) and workflow-level settings (WithOnFinish, WithOnError,
// WithDefaultRetry) both implement this type.
type WorkflowOption func(*workflowConfig)

// WithOnFinish registers a callback that is invoked after the workflow completes,
// regardless of success or failure. Callback panics are recovered and logged.
func WithOnFinish(fn func(WorkflowResult)) WorkflowOption {
	return func(c *workflowConfig) { c.onFinish = fn }
}

// WithOnError registers a callback that is invoked when a step fails.
// The callback receives the step name and the error. Callback panics are
// recovered and logged.
func WithOnError(fn func(string, error)) WorkflowOption {
	return func(c *workflowConfig) { c.onError = fn }
}

// WithDefaultRetry sets the default retry count and delay for all steps
// that do not specify their own Retry() option.
func WithDefaultRetry(n int, delay time.Duration) WorkflowOption {
	return func(c *workflowConfig) {
		c.defaultRetry = n
		c.defaultDelay = delay
	}
}

// --- Step definitions (return WorkflowOption) ---

// buildStepConfig applies step options to a base config.
func buildStepConfig(name string, fn StepFunc, st stepType, opts []StepOption) *stepConfig {
	cfg := &stepConfig{
		name:     name,
		fn:       fn,
		stepType: st,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// Step defines a workflow step that runs a StepFunc.
// The function receives the shared WorkflowContext and can read/write any keys.
// Unlike AgentStep and ToolStep, a basic Step does not automatically write output
// to context — the function is responsible for calling wCtx.Set() as needed.
func Step(name string, fn StepFunc, opts ...StepOption) WorkflowOption {
	return func(c *workflowConfig) {
		c.steps = append(c.steps, buildStepConfig(name, fn, stepTypeBasic, opts))
	}
}

// AgentStep defines a workflow step that delegates to an Agent (LLMAgent, Network,
// or another Workflow). Input is read from the context key specified by InputFrom(),
// or from WorkflowContext.Input() if InputFrom is not set.
// Output is written to context as "{name}.output" (or the key specified by OutputTo()).
// Token usage from the agent is accumulated into the workflow's total Usage.
func AgentStep(name string, agent Agent, opts ...StepOption) WorkflowOption {
	return func(c *workflowConfig) {
		cfg := buildStepConfig(name, nil, stepTypeBasic, opts)
		cfg.fn = agentStepFunc(agent, cfg)
		c.steps = append(c.steps, cfg)
	}
}

// ToolStep defines a workflow step that calls a single tool function by name.
// Args are read from the context key specified by ArgsFrom(). If ArgsFrom is not set,
// empty JSON object ({}) is used.
// The tool result is written to context as "{name}.result" (or the key specified by OutputTo()).
func ToolStep(name string, tool Tool, toolName string, opts ...StepOption) WorkflowOption {
	return func(c *workflowConfig) {
		cfg := buildStepConfig(name, nil, stepTypeBasic, opts)
		cfg.fn = toolStepFunc(tool, toolName, cfg)
		c.steps = append(c.steps, cfg)
	}
}

// ForEach defines a workflow step that runs a StepFunc once per element in a
// collection. The collection is read from the context key specified by IterOver().
// Each iteration receives the current element via ForEachItem(ctx) and its
// index via ForEachIndex(ctx). These are carried on the Go context (not WorkflowContext)
// to ensure safety under concurrent iteration.
// Concurrency defaults to 1 (sequential); use Concurrency() to parallelize.
func ForEach(name string, fn StepFunc, opts ...StepOption) WorkflowOption {
	return func(c *workflowConfig) {
		c.steps = append(c.steps, buildStepConfig(name, fn, stepTypeForEach, opts))
	}
}

// DoUntil defines a workflow step that repeats a StepFunc until the condition
// specified by Until() returns true. The condition is evaluated after each iteration.
// MaxIter() sets a safety cap (default 10).
func DoUntil(name string, fn StepFunc, opts ...StepOption) WorkflowOption {
	return func(c *workflowConfig) {
		c.steps = append(c.steps, buildStepConfig(name, fn, stepTypeDoUntil, opts))
	}
}

// DoWhile defines a workflow step that repeats a StepFunc while the condition
// function returns true. The condition is evaluated before each iteration after
// the first (the first iteration always runs). MaxIter() sets a safety cap (default 10).
// The condition is set via a dedicated StepOption — use the While() step option to
// provide the condition function.
func DoWhile(name string, fn StepFunc, opts ...StepOption) WorkflowOption {
	return func(c *workflowConfig) {
		c.steps = append(c.steps, buildStepConfig(name, fn, stepTypeDoWhile, opts))
	}
}

// While sets the continuation condition for a DoWhile step. The step repeats
// as long as the function returns true (evaluated before each iteration after
// the first).
func While(fn func(*WorkflowContext) bool) StepOption {
	return func(c *stepConfig) { c.whileFn = fn }
}

// --- ForEach iteration helpers ---

// forEachIterCtxKey is the context key for per-iteration ForEach data.
type forEachIterCtxKey struct{}

// forEachIter carries the current element and index for a ForEach iteration.
type forEachIter struct {
	item  any
	index int
}

// ForEachItem retrieves the current iteration element inside a ForEach step function.
// Returns the element and true if called from within a ForEach step, or nil and false otherwise.
// The element is carried via context.Context (not WorkflowContext), so it is safe
// for concurrent iterations — each goroutine sees its own element.
func ForEachItem(ctx context.Context) (any, bool) {
	if v, ok := ctx.Value(forEachIterCtxKey{}).(forEachIter); ok {
		return v.item, true
	}
	return nil, false
}

// ForEachIndex retrieves the current iteration index (0-based) inside a ForEach step function.
// Returns the index and true if called from within a ForEach step, or -1 and false otherwise.
func ForEachIndex(ctx context.Context) (int, bool) {
	if v, ok := ctx.Value(forEachIterCtxKey{}).(forEachIter); ok {
		return v.index, true
	}
	return -1, false
}

// --- Agent and Tool step wrappers ---

// agentStepFunc wraps an Agent into a StepFunc. Input is read from context
// (via InputFrom key) or from the original task input. Output and usage are
// written back to context.
func agentStepFunc(agent Agent, cfg *stepConfig) StepFunc {
	return func(ctx context.Context, wCtx *WorkflowContext) error {
		input := wCtx.Input()
		if cfg.inputFrom != "" {
			if v, ok := wCtx.Get(cfg.inputFrom); ok {
				input = fmt.Sprintf("%v", v)
			}
		}

		result, err := agent.Execute(ctx, AgentTask{
			Input:       input,
			Attachments: wCtx.task.Attachments,
			Context:     wCtx.task.Context,
		})
		if err != nil {
			return err
		}

		outputKey := cfg.name + ".output"
		if cfg.outputTo != "" {
			outputKey = cfg.outputTo
		}
		wCtx.Set(outputKey, result.Output)

		// Accumulate usage via atomic helper.
		wCtx.addUsage(result.Usage)

		return nil
	}
}

// toolStepFunc wraps a Tool call into a StepFunc. Args are read from context
// (via ArgsFrom key) and the tool result is written back to context.
func toolStepFunc(tool Tool, toolName string, cfg *stepConfig) StepFunc {
	return func(ctx context.Context, wCtx *WorkflowContext) error {
		var args json.RawMessage
		if cfg.argsFrom != "" {
			if v, ok := wCtx.Get(cfg.argsFrom); ok {
				switch a := v.(type) {
				case json.RawMessage:
					args = a
				case string:
					args = json.RawMessage(a)
				default:
					b, err := json.Marshal(v)
					if err != nil {
						return fmt.Errorf("step %s: marshal tool args: %w", cfg.name, err)
					}
					args = b
				}
			}
		}
		if args == nil {
			args = json.RawMessage(`{}`)
		}

		result, err := tool.Execute(ctx, toolName, args)
		if err != nil {
			return err
		}
		if result.Error != "" {
			return fmt.Errorf("tool %s: %s", toolName, result.Error)
		}

		outputKey := cfg.name + ".result"
		if cfg.outputTo != "" {
			outputKey = cfg.outputTo
		}
		wCtx.Set(outputKey, result.Content)
		return nil
	}
}

// --- Workflow struct ---

const defaultLoopMaxIter = 10

// Workflow is a deterministic, DAG-based task orchestration primitive.
// Unlike Network (which uses an LLM to route between agents), Workflow follows
// explicit step sequences and dependency edges defined at construction time.
// Parallel execution emerges naturally when multiple steps share the same
// predecessor. Workflow implements the Agent interface, enabling recursive
// composition: Networks can contain Workflows, and Workflows can contain
// Agents (LLMAgent, Network, or other Workflows).
type Workflow struct {
	name         string
	description  string
	steps        map[string]*stepConfig  // all steps by name
	stepOrder    []string                // declaration order (for deterministic iteration)
	edges        map[string][]string     // step -> its dependencies (After)
	roots        []string                // steps with no dependencies
	onFinish     func(WorkflowResult)
	onError      func(string, error)
	defaultRetry int
	defaultDelay time.Duration
}

// compile-time check
var _ Agent = (*Workflow)(nil)

// Name returns the workflow's identifier.
func (w *Workflow) Name() string { return w.name }

// Description returns a human-readable description of what the workflow does.
// Used by Network to generate tool definitions when a Workflow is used as a subagent.
func (w *Workflow) Description() string { return w.description }

// NewWorkflow creates a Workflow with the given name, description, and options.
// Step definitions and workflow-level options are passed as WorkflowOption values.
// Returns an error if the step graph is invalid:
//   - duplicate step names
//   - After() references an unknown step
//   - cycle detected in the dependency graph
//
// Logs a warning for unreachable steps (steps that are not roots and have no
// incoming edges from reachable steps).
func NewWorkflow(name, description string, opts ...WorkflowOption) (*Workflow, error) {
	var cfg workflowConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	w := &Workflow{
		name:         name,
		description:  description,
		steps:        make(map[string]*stepConfig),
		edges:        make(map[string][]string),
		onFinish:     cfg.onFinish,
		onError:      cfg.onError,
		defaultRetry: cfg.defaultRetry,
		defaultDelay: cfg.defaultDelay,
	}

	// Register steps, check for duplicates.
	for _, s := range cfg.steps {
		if _, exists := w.steps[s.name]; exists {
			return nil, fmt.Errorf("workflow %s: duplicate step name %q", name, s.name)
		}

		// Apply default retry if step doesn't have its own.
		if s.retry == 0 && w.defaultRetry > 0 {
			s.retry = w.defaultRetry
			s.retryDelay = w.defaultDelay
		}

		// Apply default maxIter for loop steps.
		if s.maxIter == 0 && (s.stepType == stepTypeDoUntil || s.stepType == stepTypeDoWhile) {
			s.maxIter = defaultLoopMaxIter
		}

		// Apply default concurrency for ForEach.
		if s.concurrency == 0 && s.stepType == stepTypeForEach {
			s.concurrency = 1
		}

		w.steps[s.name] = s
		w.stepOrder = append(w.stepOrder, s.name)
		w.edges[s.name] = s.after
	}

	// Validate dependencies: all After() targets must exist.
	for _, s := range cfg.steps {
		for _, dep := range s.after {
			if _, ok := w.steps[dep]; !ok {
				return nil, fmt.Errorf("workflow %s: step %q depends on unknown step %q", name, s.name, dep)
			}
		}
	}

	// Detect cycles via topological sort (Kahn's algorithm).
	if err := w.detectCycle(); err != nil {
		return nil, err
	}

	// Identify root steps (no dependencies).
	for _, s := range cfg.steps {
		if len(s.after) == 0 {
			w.roots = append(w.roots, s.name)
		}
	}

	// Warn about unreachable steps.
	reachable := w.findReachable()
	for _, s := range cfg.steps {
		if !reachable[s.name] {
			log.Printf(" [workflow] warning: step %q in workflow %q is unreachable", s.name, name)
		}
	}

	return w, nil
}

// detectCycle uses Kahn's algorithm for topological sorting to detect cycles.
func (w *Workflow) detectCycle() error {
	// Build in-degree map and adjacency list (dep -> dependents).
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // dep -> steps that depend on it
	for name := range w.steps {
		inDegree[name] = 0
	}
	for name, deps := range w.edges {
		inDegree[name] = len(deps)
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], name)
		}
	}

	// Start with zero in-degree nodes.
	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++
		for _, dep := range dependents[node] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if visited != len(w.steps) {
		return fmt.Errorf("workflow %s: cycle detected in step dependencies", w.name)
	}
	return nil
}

// findReachable returns the set of step names reachable from root steps
// by following dependency edges forward (dep -> dependent).
func (w *Workflow) findReachable() map[string]bool {
	// Build forward adjacency: dep -> steps that depend on it.
	dependents := make(map[string][]string)
	for name, deps := range w.edges {
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], name)
		}
	}

	reachable := make(map[string]bool)
	var visit func(string)
	visit = func(name string) {
		if reachable[name] {
			return
		}
		reachable[name] = true
		for _, next := range dependents[name] {
			visit(next)
		}
	}

	for _, root := range w.roots {
		visit(root)
	}
	return reachable
}

// --- Execution ---

// executionState tracks the runtime state of a workflow execution.
type executionState struct {
	wCtx           *WorkflowContext
	results        map[string]StepResult
	failedStep     string          // name of first step that failed, empty if none
	failureSkipped map[string]bool // steps skipped due to upstream failure (not When() condition)
	suspendedStep  string          // name of step that suspended
	suspendPayload json.RawMessage // payload from the suspended step
	mu             sync.RWMutex    // protects results, failedStep, failureSkipped
	cancel         context.CancelFunc
}

func (s *executionState) setResult(name string, sr StepResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[name] = sr
}

func (s *executionState) getResult(name string) (StepResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[name]
	return r, ok
}

// Execute runs the workflow's step graph. Steps with satisfied dependencies
// are launched concurrently. The first step failure cancels all in-flight steps
// and marks downstream steps as StepSkipped.
// Returns an AgentResult with the last successful step's output.
func (w *Workflow) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := &executionState{
		wCtx:           newWorkflowContext(task),
		results:        make(map[string]StepResult),
		failureSkipped: make(map[string]bool),
		cancel:         cancel,
	}

	// Run the DAG.
	w.runDAG(ctx, state)

	return w.buildResult(state, task)
}

// executeResume continues a suspended workflow from the given step.
func (w *Workflow) executeResume(ctx context.Context, task AgentTask, completedResults map[string]StepResult, contextValues map[string]any, _ string, data json.RawMessage) (AgentResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Reconstruct workflow context from snapshot.
	wCtx := newWorkflowContext(task)
	for k, v := range contextValues {
		wCtx.Set(k, v)
	}
	// Inject resume data for the suspended step.
	wCtx.Set(resumeDataKey, data)

	state := &executionState{
		wCtx:           wCtx,
		results:        make(map[string]StepResult),
		failureSkipped: make(map[string]bool),
		cancel:         cancel,
	}

	// Pre-populate completed steps so they don't re-execute.
	for k, v := range completedResults {
		state.results[k] = v
	}

	// Re-run the DAG — completed steps are skipped, suspended step re-executes.
	w.runDAG(ctx, state)

	// Clear resume data after execution.
	wCtx.Set(resumeDataKey, nil)

	return w.buildResult(state, task)
}

// buildResult converts execution state into an AgentResult after the DAG completes.
// Handles suspension (returns ErrSuspended), failure (returns WorkflowError),
// and success. Shared by Execute and executeResume.
func (w *Workflow) buildResult(state *executionState, task AgentTask) (AgentResult, error) {
	// Check for suspension.
	if state.suspendedStep != "" {
		snapshotResults := make(map[string]StepResult)
		for k, v := range state.results {
			if v.Status == StepSuccess || (v.Status == StepSkipped && !state.failureSkipped[k]) {
				snapshotResults[k] = v
			}
		}
		snapshotValues := make(map[string]any)
		state.wCtx.mu.RLock()
		for k, v := range state.wCtx.values {
			snapshotValues[k] = v
		}
		state.wCtx.mu.RUnlock()

		suspendedStep := state.suspendedStep
		suspendPayload := state.suspendPayload

		return AgentResult{}, &ErrSuspended{
			Step:    suspendedStep,
			Payload: suspendPayload,
			resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
				return w.executeResume(ctx, task, snapshotResults, snapshotValues, suspendedStep, data)
			},
		}
	}

	// Build WorkflowResult.
	var totalUsage Usage
	if u, ok := state.wCtx.Get("_usage"); ok {
		if usage, ok := u.(Usage); ok {
			totalUsage = usage
		}
	}

	wfStatus := StepSuccess
	var lastOutput string
	if state.failedStep != "" {
		wfStatus = StepFailed
	}
	for _, name := range w.stepOrder {
		sr, ok := state.results[name]
		if !ok {
			continue
		}
		if sr.Status == StepSuccess && sr.Output != "" {
			lastOutput = sr.Output
		}
	}

	wfResult := WorkflowResult{
		Status:  wfStatus,
		Steps:   state.results,
		Context: state.wCtx,
		Usage:   totalUsage,
	}

	if w.onFinish != nil {
		w.safeCallback(func() { w.onFinish(wfResult) })
	}

	// Convert workflow step results to StepTrace slice in execution order.
	steps := workflowStepsToTraces(w.stepOrder, state.results)

	if wfStatus == StepFailed {
		var stepErr error
		if sr, ok := state.results[state.failedStep]; ok {
			stepErr = sr.Error
		}
		return AgentResult{Output: lastOutput, Usage: totalUsage, Steps: steps}, &WorkflowError{
			StepName: state.failedStep,
			Err:      stepErr,
			Result:   wfResult,
		}
	}

	return AgentResult{Output: lastOutput, Usage: totalUsage, Steps: steps}, nil
}

// workflowStepsToTraces converts workflow StepResults into StepTrace entries
// in the order defined by stepOrder. Skipped and pending steps are omitted.
func workflowStepsToTraces(order []string, results map[string]StepResult) []StepTrace {
	var traces []StepTrace
	for _, name := range order {
		sr, ok := results[name]
		if !ok || sr.Status == StepPending || sr.Status == StepSkipped {
			continue
		}
		trace := StepTrace{
			Name:     name,
			Type:     "step",
			Output:   truncateStr(sr.Output, 500),
			Duration: sr.Duration,
		}
		if sr.Error != nil {
			trace.Output = truncateStr(sr.Error.Error(), 500)
		}
		traces = append(traces, trace)
	}
	return traces
}

// runDAG executes the step graph using a wave-based approach. Each wave
// launches all steps whose dependencies are satisfied, waits for them to
// complete, then repeats until no more steps are ready.
func (w *Workflow) runDAG(ctx context.Context, state *executionState) {
	completed := make(map[string]bool) // includes success and skipped

	for {
		// Find ready steps: not yet completed, all deps satisfied.
		var ready []*stepConfig
		for _, name := range w.stepOrder {
			if completed[name] {
				continue
			}
			// If already has a result (e.g., marked skipped), skip.
			if _, has := state.getResult(name); has {
				completed[name] = true
				continue
			}

			allSatisfied := true
			for _, dep := range w.edges[name] {
				if !completed[dep] {
					allSatisfied = false
					break
				}
			}
			if allSatisfied {
				ready = append(ready, w.steps[name])
			}
		}

		if len(ready) == 0 {
			break
		}

		// Check if any upstream failed — if so, skip downstream steps.
		var toRun []*stepConfig
		for _, s := range ready {
			if w.hasFailedUpstream(s, state) {
				state.setResult(s.name, StepResult{
					Name:   s.name,
					Status: StepSkipped,
				})
				state.mu.Lock()
				state.failureSkipped[s.name] = true
				state.mu.Unlock()
				completed[s.name] = true
			} else {
				toRun = append(toRun, s)
			}
		}

		if len(toRun) == 0 {
			continue
		}

		// Launch ready steps in parallel.
		var wg sync.WaitGroup
		for _, s := range toRun {
			wg.Add(1)
			go func(step *stepConfig) {
				defer wg.Done()
				w.executeStep(ctx, step, state)
			}(s)
		}
		wg.Wait()

		// Mark completed.
		for _, s := range toRun {
			completed[s.name] = true
		}
	}
}

// hasFailedUpstream checks if any upstream dependency of a step has failed,
// suspended, or was itself skipped due to an upstream failure. Steps skipped
// by a When() condition are treated as satisfied and do NOT propagate failure.
func (w *Workflow) hasFailedUpstream(s *stepConfig, state *executionState) bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, dep := range s.after {
		r, ok := state.results[dep]
		if !ok {
			continue
		}
		if r.Status == StepFailed || r.Status == StepSuspended {
			return true
		}
		if r.Status == StepSkipped && state.failureSkipped[dep] {
			return true
		}
	}
	return false
}

// executeStep runs a single step, handling conditions, retries, and step types.
func (w *Workflow) executeStep(ctx context.Context, s *stepConfig, state *executionState) {
	start := time.Now()

	// Check context cancellation before starting.
	if ctx.Err() != nil {
		state.setResult(s.name, StepResult{
			Name:     s.name,
			Status:   StepSkipped,
			Duration: time.Since(start),
		})
		return
	}

	// Evaluate When() condition.
	if s.when != nil && !s.when(state.wCtx) {
		state.setResult(s.name, StepResult{
			Name:     s.name,
			Status:   StepSkipped,
			Duration: time.Since(start),
		})
		log.Printf(" [workflow] step %q skipped (condition not met)", s.name)
		return
	}

	// Mark as running.
	state.setResult(s.name, StepResult{
		Name:   s.name,
		Status: StepRunning,
	})

	var run func() error
	switch s.stepType {
	case stepTypeForEach:
		run = func() error { return w.executeForEach(ctx, s, state) }
	case stepTypeDoUntil:
		run = func() error { return w.executeDoUntil(ctx, s, state) }
	case stepTypeDoWhile:
		run = func() error { return w.executeDoWhile(ctx, s, state) }
	default:
		run = func() error { return s.fn(ctx, state.wCtx) }
	}
	err := w.executeWithRetry(ctx, s, run)

	duration := time.Since(start)

	// Check for suspend (before error handling — suspend is not a failure).
	var suspend *errSuspend
	if errors.As(err, &suspend) {
		state.mu.Lock()
		state.results[s.name] = StepResult{
			Name:     s.name,
			Status:   StepSuspended,
			Duration: duration,
		}
		if state.suspendedStep == "" {
			state.suspendedStep = s.name
			state.suspendPayload = suspend.payload
		}
		state.mu.Unlock()
		log.Printf(" [workflow] step %q suspended", s.name)
		return
	}

	if err != nil {
		state.mu.Lock()
		state.results[s.name] = StepResult{
			Name:     s.name,
			Status:   StepFailed,
			Error:    err,
			Duration: duration,
		}
		if state.failedStep == "" {
			state.failedStep = s.name
		}
		state.mu.Unlock()

		log.Printf(" [workflow] step %q failed: %v", s.name, err)

		// Call onError callback.
		if w.onError != nil {
			w.safeCallback(func() { w.onError(s.name, err) })
		}

		// Fail fast: cancel context so in-flight steps get cancelled.
		state.cancel()
		return
	}

	// Read output from context for the result.
	output := w.readStepOutput(s, state.wCtx)
	state.setResult(s.name, StepResult{
		Name:     s.name,
		Status:   StepSuccess,
		Output:   output,
		Duration: duration,
	})
	log.Printf(" [workflow] step %q completed in %s", s.name, duration)
}

// executeWithRetry wraps a step execution function with retry logic.
// All step types (basic, ForEach, DoUntil, DoWhile) are routed through this.
func (w *Workflow) executeWithRetry(ctx context.Context, s *stepConfig, run func() error) error {
	maxAttempts := 1 + s.retry
	var lastErr error

	for attempt := range maxAttempts {
		if attempt > 0 {
			if s.retryDelay > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(s.retryDelay):
				}
			}
			log.Printf(" [workflow] step %q retry %d/%d", s.name, attempt, s.retry)
		}

		lastErr = run()
		if lastErr == nil {
			return nil
		}

		// Don't retry on suspend — it's not a failure.
		var susp *errSuspend
		if errors.As(lastErr, &susp) {
			return lastErr
		}

		// Don't retry on context cancellation.
		if ctx.Err() != nil {
			return lastErr
		}
	}

	return lastErr
}

// executeForEach iterates over a collection from context, running the step
// function once per element. Concurrency is bounded by the step's concurrency setting.
func (w *Workflow) executeForEach(ctx context.Context, s *stepConfig, state *executionState) error {
	if s.iterOver == "" {
		return fmt.Errorf("step %s: ForEach requires IterOver option", s.name)
	}

	v, ok := state.wCtx.Get(s.iterOver)
	if !ok {
		return fmt.Errorf("step %s: IterOver key %q not found in context", s.name, s.iterOver)
	}

	items, ok := v.([]any)
	if !ok {
		return fmt.Errorf("step %s: IterOver key %q is not []any", s.name, s.iterOver)
	}

	if len(items) == 0 {
		return nil
	}

	concurrency := s.concurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	// Cancel remaining iterations on first failure.
	iterCtx, iterCancel := context.WithCancel(ctx)
	defer iterCancel()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i, item := range items {
		select {
		case <-iterCtx.Done():
		case sem <- struct{}{}:
			wg.Add(1)
			go func(elem any, idx int) {
				defer wg.Done()
				defer func() { <-sem }()

				elemCtx := context.WithValue(iterCtx, forEachIterCtxKey{}, forEachIter{
					item:  elem,
					index: idx,
				})

				if err := s.fn(elemCtx, state.wCtx); err != nil {
					errCh <- err
					iterCancel()
				}
			}(item, i)
			continue
		}
		break // iterCtx cancelled — stop launching new iterations
	}

	wg.Wait()
	close(errCh)

	// Return the first error, if any.
	for err := range errCh {
		return err
	}
	return nil
}

// executeDoUntil repeats a step function until the condition returns true,
// capped by maxIter.
func (w *Workflow) executeDoUntil(ctx context.Context, s *stepConfig, state *executionState) error {
	if s.until == nil {
		return fmt.Errorf("step %s: DoUntil requires Until option", s.name)
	}

	maxIter := s.maxIter
	if maxIter <= 0 {
		maxIter = defaultLoopMaxIter
	}

	for i := 0; i < maxIter; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := s.fn(ctx, state.wCtx); err != nil {
			return err
		}

		if s.until(state.wCtx) {
			return nil
		}
	}

	log.Printf(" [workflow] step %q reached max iterations (%d)", s.name, maxIter)
	return nil
}

// executeDoWhile repeats a step function while the condition returns true,
// capped by maxIter. The first iteration always runs.
func (w *Workflow) executeDoWhile(ctx context.Context, s *stepConfig, state *executionState) error {
	if s.whileFn == nil {
		return fmt.Errorf("step %s: DoWhile requires While option", s.name)
	}

	maxIter := s.maxIter
	if maxIter <= 0 {
		maxIter = defaultLoopMaxIter
	}

	for i := 0; i < maxIter; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// After the first iteration, check the while condition.
		if i > 0 && !s.whileFn(state.wCtx) {
			return nil
		}

		if err := s.fn(ctx, state.wCtx); err != nil {
			return err
		}
	}

	log.Printf(" [workflow] step %q reached max iterations (%d)", s.name, maxIter)
	return nil
}

// readStepOutput reads the step's output from context based on naming conventions.
func (w *Workflow) readStepOutput(s *stepConfig, wCtx *WorkflowContext) string {
	// Try the explicit output key first.
	key := s.outputTo
	if key == "" {
		// Try common conventions.
		for _, suffix := range []string{".output", ".result"} {
			if v, ok := wCtx.Get(s.name + suffix); ok {
				return fmt.Sprintf("%v", v)
			}
		}
		return ""
	}
	if v, ok := wCtx.Get(key); ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// safeCallback runs a callback function, recovering from panics to prevent
// callback errors from affecting workflow results.
func (w *Workflow) safeCallback(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf(" [workflow] callback panic: %v", r)
		}
	}()
	fn()
}

// --- Runtime workflow definitions ---

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
	branchWhen := make(map[string]func(*WorkflowContext) bool)
	for _, n := range def.Nodes {
		if n.Type != NodeCondition {
			continue
		}
		resultKey := n.ID + ".result"
		for _, target := range n.TrueBranch {
			rk := resultKey
			branchWhen[target] = func(wCtx *WorkflowContext) bool {
				v, ok := wCtx.Get(rk)
				return ok && v == "true"
			}
		}
		for _, target := range n.FalseBranch {
			rk := resultKey
			branchWhen[target] = func(wCtx *WorkflowContext) bool {
				v, ok := wCtx.Get(rk)
				return ok && v == "false"
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
				outputKey := n.ID + ".output"
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

	var stepOpts []StepOption
	if n.OutputTo != "" {
		stepOpts = append(stepOpts, OutputTo(n.OutputTo))
	}
	if n.Retry > 0 {
		stepOpts = append(stepOpts, Retry(n.Retry, time.Second))
	}
	if when != nil {
		stepOpts = append(stepOpts, When(when))
	}

	if hasTemplates {
		// Two steps: resolver + tool call.
		resolverID := n.ID + "._args"
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

		toolStepOpts := append(stepOpts, After(resolverID), ArgsFrom(resolverID))
		toolStep := ToolStep(n.ID, tool, toolName, toolStepOpts...)

		return []WorkflowOption{resolver, toolStep}, nil
	}

	// No templates — static args.
	if len(after) > 0 {
		stepOpts = append(stepOpts, After(after...))
	}
	if len(n.Args) > 0 {
		// Store static args in a key and use ArgsFrom.
		argsKey := n.ID + "._args"
		staticArgs := Step(argsKey, func(_ context.Context, wCtx *WorkflowContext) error {
			b, err := json.Marshal(n.Args)
			if err != nil {
				return fmt.Errorf("node %s: marshal args: %w", n.ID, err)
			}
			wCtx.Set(argsKey, json.RawMessage(b))
			return nil
		}, stepOpts...)

		toolStepOpts := []StepOption{After(argsKey), ArgsFrom(argsKey)}
		if n.OutputTo != "" {
			toolStepOpts = append(toolStepOpts, OutputTo(n.OutputTo))
		}
		if n.Retry > 0 {
			toolStepOpts = append(toolStepOpts, Retry(n.Retry, time.Second))
		}
		return []WorkflowOption{staticArgs, ToolStep(n.ID, tool, toolName, toolStepOpts...)}, nil
	}

	return []WorkflowOption{ToolStep(n.ID, tool, toolName, stepOpts...)}, nil
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

	resultKey := n.ID + ".result"
	expr := n.Expression

	condStep := Step(n.ID, func(_ context.Context, wCtx *WorkflowContext) error {
		// Check registered condition functions first.
		if fn, ok := reg.Conditions[expr]; ok {
			if fn(wCtx) {
				wCtx.Set(resultKey, "true")
			} else {
				wCtx.Set(resultKey, "false")
			}
			return nil
		}

		result, err := evalExpression(expr, wCtx)
		if err != nil {
			return fmt.Errorf("node %s: %w", n.ID, err)
		}
		if result {
			wCtx.Set(resultKey, "true")
		} else {
			wCtx.Set(resultKey, "false")
		}
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

	outputKey := n.ID + ".output"
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
func evalExpression(expr string, wCtx *WorkflowContext) (bool, error) {
	resolved := wCtx.Resolve(expr)

	for _, op := range expressionOperators {
		idx := strings.Index(resolved, op)
		if idx == -1 {
			continue
		}

		left := strings.TrimSpace(resolved[:idx])
		right := strings.TrimSpace(resolved[idx+len(op):])
		left = stripQuotes(left)
		right = stripQuotes(right)

		return evalCompare(left, right, op)
	}

	return false, fmt.Errorf("expression: no operator found in %q", expr)
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
		return false, nil
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
