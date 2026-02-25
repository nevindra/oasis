package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Context key conventions for step output/result naming.
const (
	usageKey          = "_usage"
	outputSuffix      = ".output"
	resultSuffix      = ".result"
	argResolverSuffix = "._args"
)

// stringifyValue converts a context value to a string. Uses a type-switch fast
// path for string values to avoid the allocation from fmt.Sprintf("%v", v).
func stringifyValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

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
	if existing, ok := c.values[usageKey]; ok {
		if eu, ok := existing.(Usage); ok {
			u.InputTokens += eu.InputTokens
			u.OutputTokens += eu.OutputTokens
		}
	}
	c.values[usageKey] = u
}

// Resolve replaces {{key}} placeholders in template with values from the
// context's values map. Unknown keys resolve to empty strings. Values are
// converted to strings via stringifyValue. If the template contains no
// placeholders, it is returned as-is.
//
// Security: Resolve performs a single pass — resolved values are NOT
// re-expanded even if they contain "{{...}}". However, callers must not
// build templates by concatenating untrusted input, as that could inject
// arbitrary placeholders before Resolve runs. Use Set/Get for untrusted
// data and keep templates as compile-time constants or definition-time strings.
func (c *WorkflowContext) Resolve(template string) string {
	if !strings.Contains(template, "{{") {
		return template
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	var b strings.Builder
	b.Grow(len(template))
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
			b.WriteString(stringifyValue(v))
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

// ErrMaxIterExceeded is returned by DoUntil/DoWhile steps when the loop cap
// is reached without the exit condition being met.
var ErrMaxIterExceeded = errors.New("step reached max iterations without meeting exit condition")

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
	name       string
	fn         StepFunc
	after      []string                   // dependency edges
	when       func(*WorkflowContext) bool // conditional execution gate
	inputFrom  string                      // AgentStep: context key for input
	argsFrom   string                      // ToolStep: context key for args
	outputTo   string                      // override default output key
	retry      int                         // max retry count (0 = no retries)
	retryDelay time.Duration               // delay between retries

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
	tracer       Tracer
	logger       *slog.Logger
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

// While sets the continuation condition for a DoWhile step. The step repeats
// as long as the function returns true (evaluated before each iteration after
// the first).
func While(fn func(*WorkflowContext) bool) StepOption {
	return func(c *stepConfig) { c.whileFn = fn }
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

// WithWorkflowTracer sets the tracer for the workflow. When set, the workflow
// emits spans for execution and step lifecycle events.
func WithWorkflowTracer(t Tracer) WorkflowOption {
	return func(c *workflowConfig) { c.tracer = t }
}

// WithWorkflowLogger sets the structured logger for the workflow.
// If not set, a no-op logger is used (no output).
func WithWorkflowLogger(l *slog.Logger) WorkflowOption {
	return func(c *workflowConfig) { c.logger = l }
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
	dependents   map[string][]string     // forward adjacency: dep -> steps that depend on it
	roots        []string                // steps with no dependencies
	onFinish     func(WorkflowResult)
	onError      func(string, error)
	defaultRetry int
	defaultDelay time.Duration
	tracer       Tracer
	logger       *slog.Logger
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

	logger := cfg.logger
	if logger == nil {
		logger = nopLogger
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
		tracer:       cfg.tracer,
		logger:       logger,
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

	// Build forward adjacency (dep -> dependents) and detect cycles.
	w.dependents = w.buildDependents()
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
			logger.Warn("unreachable step", "workflow", name, "step", s.name)
		}
	}

	return w, nil
}

// buildDependents constructs the forward adjacency map (dep -> steps that
// depend on it) from the edges map. Computed once at construction time and
// reused by detectCycle, findReachable, and runDAG.
func (w *Workflow) buildDependents() map[string][]string {
	dependents := make(map[string][]string, len(w.steps))
	for name, deps := range w.edges {
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], name)
		}
	}
	return dependents
}

// detectCycle uses Kahn's algorithm for topological sorting to detect cycles.
// Requires w.dependents to be populated (via buildDependents) before calling.
func (w *Workflow) detectCycle() error {
	inDegree := make(map[string]int, len(w.steps))
	for name := range w.steps {
		inDegree[name] = len(w.edges[name])
	}

	// Start with zero in-degree nodes. Use an index-based queue to avoid
	// retaining popped elements in the backing array.
	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	visited := 0
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		visited++
		for _, dep := range w.dependents[node] {
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
// by following the pre-computed forward adjacency (w.dependents).
func (w *Workflow) findReachable() map[string]bool {
	reachable := make(map[string]bool, len(w.steps))
	var visit func(string)
	visit = func(name string) {
		if reachable[name] {
			return
		}
		reachable[name] = true
		for _, next := range w.dependents[name] {
			visit(next)
		}
	}

	for _, root := range w.roots {
		visit(root)
	}
	return reachable
}
