package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed prelude.py
var pyPreludeSource string

//go:embed prelude.js
var jsPreludeSource string

const pyPostludeSource = `
if _output_files:
    import json as _j
    _proto_out.write(_j.dumps({"type": "result_files", "files": _output_files}) + '\n')
    _proto_out.flush()
if _final_result is not None:
    import json as _j
    _proto_out.write(_j.dumps({"type": "result", "data": _final_result}) + '\n')
    _proto_out.flush()
`

const jsPostludeSource = `
} catch (_err) {
    console.error(_err.stack || _err.message || String(_err));
    process.exitCode = 1;
}
// Write protocol messages after user code completes.
if (_outputFiles.length > 0) {
    _protoOut.write(JSON.stringify({ type: 'result_files', files: _outputFiles }) + '\n');
}
if (_finalResult !== undefined) {
    _protoOut.write(JSON.stringify({ type: 'result', data: _finalResult }) + '\n');
}
})();
`

// runner executes code in a subprocess (Python or Node.js).
type runner struct {
	pythonBin string
	nodeBin   string
	maxOutput int
}

func newRunner(pythonBin, nodeBin string, maxOutput int) *runner {
	if maxOutput <= 0 {
		maxOutput = 512 * 1024
	}
	return &runner{pythonBin: pythonBin, nodeBin: nodeBin, maxOutput: maxOutput}
}

// runRequest carries parameters for a single code execution.
type runRequest struct {
	code         string
	runtime      string // "python" or "node"
	workspaceDir string
	callbackURL  string
	executionID  string
	timeout      time.Duration
}

// runResult is the outcome of a subprocess execution.
type runResult struct {
	stdout   string   // JSON-encoded set_result() data
	stderr   string   // captured print() output
	exitCode int
	err      string   // timeout/process error message
	files    []string // relative paths declared via set_result(files=[...])
}

// run executes the given code in a subprocess.
func (r *runner) run(ctx context.Context, req runRequest) runResult {
	ctx, cancel := context.WithTimeout(ctx, req.timeout)
	defer cancel()

	// Select runtime binary, prelude, postlude, and file extension.
	var bin, prelude, postlude, ext string
	switch req.runtime {
	case "node":
		bin = r.nodeBin
		prelude = jsPreludeSource
		postlude = jsPostludeSource
		ext = "sandbox-*.js"
	default: // "python" or ""
		bin = r.pythonBin
		prelude = pyPreludeSource
		postlude = pyPostludeSource
		ext = "sandbox-*.py"
	}

	script := prelude + "\n" + req.code + "\n" + postlude

	tmpFile, err := os.CreateTemp(req.workspaceDir, ext)
	if err != nil {
		return runResult{err: "create temp file: " + err.Error(), exitCode: -1}
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(script); err != nil {
		tmpFile.Close()
		return runResult{err: "write script: " + err.Error(), exitCode: -1}
	}
	tmpFile.Close()

	cmd := exec.CommandContext(ctx, bin, tmpPath)
	cmd.Dir = req.workspaceDir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=en_US.UTF-8",
		"_SANDBOX_CALLBACK_URL=" + req.callbackURL,
		"_SANDBOX_EXECUTION_ID=" + req.executionID,
		"_SANDBOX_WORKSPACE=" + req.workspaceDir,
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runResult{err: "stdout pipe: " + err.Error(), exitCode: -1}
	}

	var stderrBuf limitedWriter
	stderrBuf.limit = r.maxOutput
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return runResult{err: "start subprocess: " + err.Error(), exitCode: -1}
	}

	var resultJSON string
	var resultFiles []string

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, r.maxOutput), r.maxOutput)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var msg struct {
			Type  string   `json:"type"`
			Data  any      `json:"data"`
			Files []string `json:"files"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "result":
			b, _ := json.Marshal(msg.Data)
			resultJSON = string(b)
		case "result_files":
			resultFiles = msg.Files
		}
	}

	waitErr := cmd.Wait()
	logs := stderrBuf.String()

	res := runResult{
		stdout: resultJSON,
		stderr: logs,
		files:  resultFiles,
	}

	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			res.err = fmt.Sprintf("execution timed out after %s", req.timeout)
			res.exitCode = -1
		} else if exitErr, ok := waitErr.(*exec.ExitError); ok {
			res.exitCode = exitErr.ExitCode()
			// Include stderr in error for LLM self-correction.
			res.err = logs
		} else {
			res.err = waitErr.Error()
			res.exitCode = -1
		}
	}

	return res
}

// collectOutputFiles reads declared files from the workspace and base64-encodes them.
func collectOutputFiles(workspaceDir string, paths []string) []outputFile {
	if len(paths) == 0 {
		return nil
	}
	var out []outputFile
	for _, p := range paths {
		clean := filepath.Join(workspaceDir, filepath.Clean("/"+p))
		if !strings.HasPrefix(clean, workspaceDir+string(filepath.Separator)) {
			continue
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			continue
		}
		out = append(out, outputFile{
			Name: filepath.Base(p),
			MIME: detectMIME(p, data),
			Data: base64.StdEncoding.EncodeToString(data),
		})
	}
	return out
}

// detectMIME returns a MIME type for the given filename and data.
func detectMIME(name string, data []byte) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html"
	case ".txt", ".log":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".zip":
		return "application/zip"
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	return http.DetectContentType(sniff)
}

// writeInputFiles decodes base64-encoded input files and writes them to the workspace.
func writeInputFiles(workspaceDir string, files []inputFile) error {
	for _, f := range files {
		if f.Name == "" {
			continue
		}
		clean := filepath.Join(workspaceDir, filepath.Clean("/"+f.Name))
		if !strings.HasPrefix(clean, workspaceDir+string(filepath.Separator)) {
			return fmt.Errorf("invalid file path: %q", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o750); err != nil {
			return fmt.Errorf("mkdir for %q: %w", f.Name, err)
		}
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return fmt.Errorf("decode %q: %w", f.Name, err)
		}
		if err := os.WriteFile(clean, data, 0o640); err != nil {
			return fmt.Errorf("write %q: %w", f.Name, err)
		}
	}
	return nil
}

// limitedWriter captures up to limit bytes and discards the rest.
type limitedWriter struct {
	buf   strings.Builder
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.buf.Len() < w.limit {
		remaining := w.limit - w.buf.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *limitedWriter) String() string { return w.buf.String() }
