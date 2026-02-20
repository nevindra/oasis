package oasis

import (
	"context"
	"time"
)

// CodeRunner executes code written by an LLM in a sandboxed environment.
// Implementations control the runtime (subprocess, container, Wasm).
// The dispatch function bridges code back to the agent's tool registry,
// enabling code to call any tool the agent has access to.
type CodeRunner interface {
	// Run executes code and returns the result. The dispatch function
	// allows code to call agent tools via call_tool() from within the code.
	Run(ctx context.Context, req CodeRequest, dispatch DispatchFunc) (CodeResult, error)
}

// CodeRequest is the input to CodeRunner.Run.
type CodeRequest struct {
	// Code is the source code to execute.
	Code string `json:"code"`
	// Timeout is the maximum execution duration. Zero means use runner default.
	Timeout time.Duration `json:"-"`
}

// CodeResult is the output of CodeRunner.Run.
type CodeResult struct {
	// Output is the structured result set via set_result() in code.
	Output string `json:"output"`
	// Logs captures print() output and stderr from the code execution.
	Logs string `json:"logs,omitempty"`
	// ExitCode is the process exit code (0 = success).
	ExitCode int `json:"exit_code"`
	// Error describes execution failure (timeout, syntax error, etc).
	Error string `json:"error,omitempty"`
}
