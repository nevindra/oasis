package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	oasis "github.com/nevindra/oasis"
)

// Tool executes shell commands in a sandboxed workspace.
type Tool struct {
	workspacePath  string
	defaultTimeout int // seconds
}

// New creates a ShellTool. Commands run in workspacePath with the given default timeout.
func New(workspacePath string, defaultTimeout int) *Tool {
	if defaultTimeout <= 0 {
		defaultTimeout = 30
	}
	return &Tool{workspacePath: workspacePath, defaultTimeout: defaultTimeout}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{{
		Name:        "shell_exec",
		Description: "Execute a shell command in the workspace directory. Returns stdout + stderr. Use for running scripts, checking files, or system tasks.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to execute"},"timeout":{"type":"integer","description":"Timeout in seconds (default 30)"}},"required":["command"]}`),
	}}
}

func (t *Tool) Execute(ctx context.Context, _ string, args json.RawMessage) (oasis.ToolResult, error) {
	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}

	if params.Command == "" {
		return oasis.ToolResult{Error: "command is required"}, nil
	}

	// Basic blocklist
	lower := strings.ToLower(params.Command)
	blocked := []string{"rm -rf /", "sudo ", "mkfs", "> /dev/", "dd if="}
	for _, b := range blocked {
		if strings.Contains(lower, b) {
			return oasis.ToolResult{Error: "command blocked for safety: " + b}, nil
		}
	}

	timeout := t.defaultTimeout
	if params.Timeout > 0 {
		timeout = params.Timeout
	}
	if timeout > 300 {
		timeout = 300
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", params.Command)
	cmd.Dir = t.workspacePath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var output string
	if stdout.Len() > 0 {
		output = stdout.String()
	}
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n--- stderr ---\n"
		}
		output += stderr.String()
	}

	// Truncate
	if len(output) > 4000 {
		output = output[:4000] + "\n... (truncated)"
	}

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return oasis.ToolResult{Content: output, Error: fmt.Sprintf("command timed out after %ds", timeout)}, nil
		}
		if output == "" {
			output = err.Error()
		}
		return oasis.ToolResult{Content: output, Error: "exit: " + err.Error()}, nil
	}

	if output == "" {
		output = "(no output)"
	}

	return oasis.ToolResult{Content: output}, nil
}
