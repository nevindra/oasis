package code

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	oasis "github.com/nevindra/oasis"
)

//go:embed prelude.py
var preludeSource string

// postludeSource is appended after user code to flush the final result.
const postludeSource = `
if _final_result is not None:
    import json as _json_post
    _msg = _json_post.dumps({"type": "result", "data": _final_result})
    _proto_out.write(_msg + '\n')
    _proto_out.flush()
`

// blockedPatterns are checked before execution to reject obviously dangerous code.
var blockedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`os\.system\s*\(`),
	regexp.MustCompile(`subprocess\.\w+\s*\(`),
}

// SubprocessRunner executes Python code in a subprocess with a JSON protocol
// bridge for tool calls. Implements oasis.CodeRunner.
type SubprocessRunner struct {
	pythonBin string
	cfg       runnerConfig
}

// compile-time check
var _ oasis.CodeRunner = (*SubprocessRunner)(nil)

// NewSubprocessRunner creates a SubprocessRunner that executes Python code
// via the given Python binary (e.g., "python3").
func NewSubprocessRunner(pythonBin string, opts ...Option) *SubprocessRunner {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return &SubprocessRunner{pythonBin: pythonBin, cfg: cfg}
}

// Run executes Python code in a subprocess. The dispatch function bridges
// call_tool() calls in Python back to the agent's tool registry.
func (r *SubprocessRunner) Run(ctx context.Context, req oasis.CodeRequest, dispatch oasis.DispatchFunc) (oasis.CodeResult, error) {
	// Pre-execution blocklist check.
	for _, pat := range blockedPatterns {
		if pat.MatchString(req.Code) {
			return oasis.CodeResult{
				Error:    fmt.Sprintf("blocked: code contains prohibited pattern: %s", pat.String()),
				ExitCode: 1,
			}, nil
		}
	}

	// Determine timeout.
	timeout := r.cfg.timeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Write temp script: prelude + user code + postlude.
	tmpFile, err := os.CreateTemp("", "oasis-code-*.py")
	if err != nil {
		return oasis.CodeResult{}, fmt.Errorf("code runner: create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	script := preludeSource + "\n" + req.Code + "\n" + postludeSource
	if _, err := tmpFile.WriteString(script); err != nil {
		tmpFile.Close()
		return oasis.CodeResult{}, fmt.Errorf("code runner: write script: %w", err)
	}
	tmpFile.Close()

	// Build subprocess.
	cmd := exec.CommandContext(ctx, r.pythonBin, tmpFile.Name())
	cmd.Dir = r.resolveWorkspace()
	cmd.Env = r.buildEnv()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return oasis.CodeResult{}, fmt.Errorf("code runner: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return oasis.CodeResult{}, fmt.Errorf("code runner: stdout pipe: %w", err)
	}

	// Capture stderr (print() output + error messages).
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrWriter{w: &stderrBuf, max: r.cfg.maxOutput}

	if err := cmd.Start(); err != nil {
		return oasis.CodeResult{}, fmt.Errorf("code runner: start subprocess: %w", err)
	}

	// Protocol loop: read JSON messages from stdout, dispatch tool calls,
	// write results to stdin.
	var finalOutput string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, r.cfg.maxOutput), r.cfg.maxOutput)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg protocolMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue // skip malformed lines
		}

		switch msg.Type {
		case "tool_call":
			result := r.handleToolCall(ctx, msg, dispatch)
			writeJSON(stdin, result)

		case "tool_calls_parallel":
			result := r.handleToolCallsParallel(ctx, msg, dispatch)
			writeJSON(stdin, result)

		case "result":
			data, _ := json.Marshal(msg.Data)
			finalOutput = string(data)
		}
	}

	// Wait for process to exit.
	err = cmd.Wait()
	logs := stderrBuf.String()

	// Truncate logs if too large.
	if len(logs) > r.cfg.maxOutput {
		logs = logs[:r.cfg.maxOutput] + "\n... (truncated)"
	}

	result := oasis.CodeResult{
		Output: finalOutput,
		Logs:   logs,
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.Error = fmt.Sprintf("execution timed out after %s", timeout)
			result.ExitCode = -1
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			result.Error = fmt.Sprintf("exit code %d", exitErr.ExitCode())
		} else {
			result.Error = err.Error()
			result.ExitCode = -1
		}
	}

	return result, nil
}

// resolveWorkspace returns the working directory for the subprocess.
func (r *SubprocessRunner) resolveWorkspace() string {
	if r.cfg.workspace != "" {
		return r.cfg.workspace
	}
	return os.TempDir()
}

// buildEnv constructs the environment for the subprocess.
func (r *SubprocessRunner) buildEnv() []string {
	var env []string
	if r.cfg.envPassthrough {
		env = os.Environ()
	} else {
		// Minimal environment for Python to work.
		env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"LANG=en_US.UTF-8",
		}
	}

	// Set workspace for prelude safety guards.
	workspace := r.resolveWorkspace()
	env = append(env, "_OASIS_WORKSPACE="+workspace)

	// Add user-specified env vars.
	for k, v := range r.cfg.envVars {
		env = append(env, k+"="+v)
	}

	return env
}

// --- Protocol types ---

type protocolMessage struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Args  json.RawMessage `json:"args,omitempty"`
	Calls []protocolCall  `json:"calls,omitempty"`
	Data  any             `json:"data,omitempty"`
}

type protocolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type protocolResponse struct {
	Type    string           `json:"type"`
	ID      string           `json:"id,omitempty"`
	Data    string           `json:"data,omitempty"`
	Error   string           `json:"error,omitempty"`
	Results []protocolResult `json:"results,omitempty"`
}

type protocolResult struct {
	ID    string `json:"id"`
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// handleToolCall dispatches a single tool call and returns the protocol response.
func (r *SubprocessRunner) handleToolCall(ctx context.Context, msg protocolMessage, dispatch oasis.DispatchFunc) protocolResponse {
	// Prevent recursion: execute_code cannot call execute_code.
	if msg.Name == "execute_code" {
		return protocolResponse{
			Type:  "tool_error",
			ID:    msg.ID,
			Error: "execute_code cannot call execute_code (no recursion)",
		}
	}

	dr := dispatch(ctx, oasis.ToolCall{
		ID:   msg.ID,
		Name: msg.Name,
		Args: msg.Args,
	})

	if len(dr.Content) > 7 && dr.Content[:7] == "error: " {
		return protocolResponse{
			Type:  "tool_error",
			ID:    msg.ID,
			Error: dr.Content[7:],
		}
	}
	return protocolResponse{
		Type: "tool_result",
		ID:   msg.ID,
		Data: dr.Content,
	}
}

// handleToolCallsParallel dispatches multiple tool calls in parallel.
func (r *SubprocessRunner) handleToolCallsParallel(ctx context.Context, msg protocolMessage, dispatch oasis.DispatchFunc) protocolResponse {
	// Build ToolCall slice, preventing recursion.
	calls := make([]oasis.ToolCall, len(msg.Calls))
	for i, c := range msg.Calls {
		if c.Name == "execute_code" {
			return protocolResponse{
				Type:  "tool_error",
				ID:    c.ID,
				Error: "execute_code cannot call execute_code (no recursion)",
			}
		}
		calls[i] = oasis.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args}
	}

	// Parallel dispatch using goroutines.
	results := make([]protocolResult, len(calls))
	type indexedResult struct {
		idx     int
		content string
	}
	ch := make(chan indexedResult, len(calls))
	for i, tc := range calls {
		go func(idx int, tc oasis.ToolCall) {
			dr := dispatch(ctx, tc)
			ch <- indexedResult{idx: idx, content: dr.Content}
		}(i, tc)
	}
	for range calls {
		ir := <-ch
		c := msg.Calls[ir.idx]
		pr := protocolResult{ID: c.ID, Data: ir.content}
		if len(ir.content) > 7 && ir.content[:7] == "error: " {
			pr.Data = ""
			pr.Error = ir.content[7:]
		}
		results[ir.idx] = pr
	}

	return protocolResponse{
		Type:    "tool_results_parallel",
		Results: results,
	}
}

// writeJSON writes a JSON-encoded message to the writer, followed by a newline.
func writeJSON(w io.Writer, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "%s\n", data)
}

// stderrWriter limits stderr capture to a maximum size.
type stderrWriter struct {
	w   *strings.Builder
	max int
}

func (sw *stderrWriter) Write(p []byte) (int, error) {
	if sw.w.Len() < sw.max {
		remaining := sw.max - sw.w.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		sw.w.Write(p)
	}
	return len(p), nil
}
