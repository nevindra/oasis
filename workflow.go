package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	mu     sync.RWMutex
}

// newWorkflowContext creates a WorkflowContext initialized with the original task input.
func newWorkflowContext(input string) *WorkflowContext {
	return &WorkflowContext{
		values: make(map[string]any),
		input:  input,
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

		result, err := agent.Execute(ctx, AgentTask{Input: input})
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
	mu             sync.Mutex      // protects results, failedStep, failureSkipped
	cancel         context.CancelFunc
}

func (s *executionState) setResult(name string, sr StepResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[name] = sr
}

func (s *executionState) getResult(name string) (StepResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		wCtx:           newWorkflowContext(task.Input),
		results:        make(map[string]StepResult),
		failureSkipped: make(map[string]bool),
		cancel:         cancel,
	}

	// Run the DAG.
	w.runDAG(ctx, state)

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

	// Call onFinish callback.
	if w.onFinish != nil {
		w.safeCallback(func() { w.onFinish(wfResult) })
	}

	if wfStatus == StepFailed && lastOutput == "" {
		// Use failedStep for a direct error summary.
		if sr, ok := state.results[state.failedStep]; ok && sr.Error != nil {
			lastOutput = fmt.Sprintf("workflow %s failed at step %s: %s", w.name, state.failedStep, sr.Error)
		}
	}

	return AgentResult{Output: lastOutput, Usage: totalUsage}, nil
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

// hasFailedUpstream checks if any upstream dependency of a step has failed or
// was itself skipped due to an upstream failure. Steps skipped by a When()
// condition are treated as satisfied and do NOT propagate failure.
func (w *Workflow) hasFailedUpstream(s *stepConfig, state *executionState) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, dep := range s.after {
		r, ok := state.results[dep]
		if !ok {
			continue
		}
		if r.Status == StepFailed {
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

	var err error
	switch s.stepType {
	case stepTypeForEach:
		err = w.executeForEach(ctx, s, state)
	case stepTypeDoUntil:
		err = w.executeDoUntil(ctx, s, state)
	case stepTypeDoWhile:
		err = w.executeDoWhile(ctx, s, state)
	default:
		err = w.executeWithRetry(ctx, s, state)
	}

	duration := time.Since(start)

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

// executeWithRetry runs a step function with retry logic.
func (w *Workflow) executeWithRetry(ctx context.Context, s *stepConfig, state *executionState) error {
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

		lastErr = s.fn(ctx, state.wCtx)
		if lastErr == nil {
			return nil
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

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	errCh := make(chan error, len(items))

	for i, item := range items {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(elem any, idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			// Carry the element via context.Context (per-goroutine, no race).
			// Step functions retrieve it via ForEachItem(ctx).
			iterCtx := context.WithValue(ctx, forEachIterCtxKey{}, forEachIter{
				item:  elem,
				index: idx,
			})

			if err := s.fn(iterCtx, state.wCtx); err != nil {
				errCh <- err
			}
		}(item, i)
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
