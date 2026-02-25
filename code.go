package oasis

import (
	"context"
	"time"
)

// CodeRunner executes code written by an LLM in a sandboxed environment.
// Implementations control the runtime (HTTP sandbox, container, Wasm).
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
	// Runtime selects the execution environment ("python", "node").
	// Empty defaults to "python".
	Runtime string `json:"runtime,omitempty"`
	// Timeout is the maximum execution duration. Zero means use runner default.
	Timeout time.Duration `json:"-"`
	// SessionID enables workspace persistence across executions.
	// Same session ID = same workspace directory. Empty = isolated per execution.
	SessionID string `json:"session_id,omitempty"`
	// Files are placed in the workspace before execution.
	// For input: populate Name + Data (inline) or Name + URL (sandbox downloads).
	Files []CodeFile `json:"files,omitempty"`
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
	// Files are explicitly returned by the code via set_result(files=[...]).
	Files []CodeFile `json:"files,omitempty"`
}

// CodeFile represents a file transferred between app and sandbox.
//
// For input: Name + Data (inline bytes) or Name + URL (sandbox downloads via HTTP GET).
// For output: Name + MIME + Data (always inline).
type CodeFile struct {
	// Name is the filename (e.g. "chart.png", "data.csv").
	Name string `json:"name"`
	// MIME is the media type (e.g. "image/png"). Set on output files.
	MIME string `json:"mime,omitempty"`
	// Data holds inline file bytes. Tagged json:"-" to avoid double-encoding;
	// wire format uses base64 in a separate field.
	Data []byte `json:"-"`
	// URL is an alternative to Data: the sandbox downloads via HTTP GET.
	// Future: not yet implemented by the reference sandbox.
	URL string `json:"url,omitempty"`
}
