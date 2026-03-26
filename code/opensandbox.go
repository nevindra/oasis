package code

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	oasis "github.com/nevindra/oasis"
)

//go:embed os_prelude.py
var osPyPrelude string

//go:embed os_prelude.js
var osJsPrelude string

const osPyPostlude = `
if _output_files:
    import json as _j
    _proto_out.write(_j.dumps({"type": "result_files", "files": _output_files}) + '\n')
    _proto_out.flush()
if _final_result is not None:
    import json as _j
    _proto_out.write(_j.dumps({"type": "result", "data": _final_result}) + '\n')
    _proto_out.flush()
`

const osJsPostlude = `
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

// OpenSandboxRunner executes code in remote OpenSandbox containers.
// It implements oasis.CodeRunner.
//
// Each session maps to a sandbox container. On first Run(), the callback
// server starts automatically unless WithExternalCallbackAddr was configured.
type OpenSandboxRunner struct {
	cfg       openSandboxConfig
	api       *osAPI
	server    *callbackServer
	startOnce sync.Once
	startErr  error
	mu        sync.Mutex
	sandboxes map[string]*sandboxInstance
}

// compile-time check
var _ oasis.CodeRunner = (*OpenSandboxRunner)(nil)

// NewOpenSandboxRunner creates an OpenSandboxRunner that manages sandbox
// containers via the OpenSandbox API at serverURL.
func NewOpenSandboxRunner(serverURL, apiKey string, opts ...OpenSandboxOption) *OpenSandboxRunner {
	cfg := defaultOpenSandboxConfig()
	cfg.serverURL = strings.TrimRight(serverURL, "/")
	cfg.apiKey = apiKey
	for _, o := range opts {
		o(&cfg)
	}

	return &OpenSandboxRunner{
		cfg:       cfg,
		api:       newOSAPI(cfg.serverURL, cfg.apiKey, cfg.execdToken),
		server:    newCallbackServer(),
		sandboxes: make(map[string]*sandboxInstance),
	}
}

// Handler returns the http.Handler for the /_oasis/dispatch endpoint.
// Mount this on your own mux when using WithExternalCallbackAddr:
//
//	mux.Handle("/_oasis/dispatch", runner.Handler())
func (r *OpenSandboxRunner) Handler() http.Handler {
	return r.server.Handler()
}

// Close deletes all managed sandboxes and shuts down the callback server.
func (r *OpenSandboxRunner) Close() error {
	r.mu.Lock()
	sandboxes := make(map[string]*sandboxInstance, len(r.sandboxes))
	for k, v := range r.sandboxes {
		sandboxes[k] = v
	}
	r.sandboxes = make(map[string]*sandboxInstance)
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, sb := range sandboxes {
		r.api.deleteSandbox(ctx, sb.id) //nolint:errcheck
	}

	return r.server.Close()
}

// ensureStarted lazily starts the callback server on first Run().
func (r *OpenSandboxRunner) ensureStarted() error {
	r.startOnce.Do(func() {
		if r.cfg.callbackExtAddr != "" {
			return
		}
		r.startErr = r.server.Start(r.cfg.callbackAddr)
	})
	return r.startErr
}

// callbackURL returns the full URL the sandbox should POST tool calls to.
func (r *OpenSandboxRunner) callbackURL() string {
	if r.cfg.callbackExtAddr != "" {
		return strings.TrimRight(r.cfg.callbackExtAddr, "/") + callbackPath
	}
	return "http://" + r.server.Addr() + callbackPath
}

// ensureSandbox returns an existing sandbox for the session key, or creates one.
// Uses double-check locking to avoid duplicate sandbox creation.
func (r *OpenSandboxRunner) ensureSandbox(ctx context.Context, sessionKey string) (*sandboxInstance, error) {
	// Fast path: check if sandbox already exists.
	r.mu.Lock()
	if sb, ok := r.sandboxes[sessionKey]; ok {
		r.mu.Unlock()
		return sb, nil
	}
	r.mu.Unlock()

	// Slow path: create a new sandbox.
	createReq := osCreateRequest{
		Image:          osImage{URI: r.cfg.image},
		ResourceLimits: osResourceLimits{CPU: r.cfg.resourceCPU, Memory: r.cfg.resourceMem},
		Entrypoint:     r.cfg.entrypoint,
		Env:            r.cfg.sandboxEnv,
	}
	if r.cfg.sandboxTTL > 0 {
		ttl := r.cfg.sandboxTTL
		createReq.Timeout = &ttl
	}

	sandbox, err := r.api.createSandbox(ctx, createReq)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}

	// Get the execd endpoint (port 44772 is the execd default).
	ep, err := r.api.getEndpoint(ctx, sandbox.ID, 44772)
	if err != nil {
		// Clean up the sandbox we just created.
		r.api.deleteSandbox(ctx, sandbox.ID) //nolint:errcheck
		return nil, fmt.Errorf("get execd endpoint: %w", err)
	}

	sb := &sandboxInstance{
		id:      sandbox.ID,
		execd:   strings.TrimRight(ep.Endpoint, "/"),
		headers: ep.Headers,
	}

	// Wait for the sandbox to be ready.
	readyCtx, readyCancel := context.WithTimeout(ctx, 60*time.Second)
	defer readyCancel()
	if err := r.api.waitReady(readyCtx, sb); err != nil {
		r.api.deleteSandbox(ctx, sandbox.ID) //nolint:errcheck
		return nil, fmt.Errorf("sandbox not ready: %w", err)
	}

	// Double-check: another goroutine may have created one while we were waiting.
	r.mu.Lock()
	if existing, ok := r.sandboxes[sessionKey]; ok {
		r.mu.Unlock()
		// Clean up the duplicate we just created.
		r.api.deleteSandbox(ctx, sandbox.ID) //nolint:errcheck
		return existing, nil
	}
	r.sandboxes[sessionKey] = sb
	r.mu.Unlock()

	return sb, nil
}

// Run executes code in an OpenSandbox container.
// Implements oasis.CodeRunner.
func (r *OpenSandboxRunner) Run(ctx context.Context, req oasis.CodeRequest, dispatch oasis.DispatchFunc) (oasis.CodeResult, error) {
	if err := r.ensureStarted(); err != nil {
		return oasis.CodeResult{}, err
	}

	// Determine timeout.
	timeout := r.cfg.execTimeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Generate execution ID for callback correlation.
	executionID := oasis.NewID()

	// Build callback URL.
	cbURL := r.callbackURL()

	// Resolve session key: empty = ephemeral (unique per execution).
	sessionKey := req.SessionID
	ephemeral := sessionKey == ""
	if ephemeral {
		sessionKey = "ephemeral_" + executionID
	}

	// Ensure sandbox exists for this session.
	sb, err := r.ensureSandbox(ctx, sessionKey)
	if err != nil {
		return oasis.CodeResult{}, fmt.Errorf("sandbox setup: %w", err)
	}

	// Upload input files to /workspace/.
	for _, f := range req.Files {
		if f.Name == "" {
			continue
		}
		remotePath := "/workspace/" + f.Name
		if err := r.api.uploadFile(ctx, sb, osFileMetadata{Path: remotePath}, f.Data); err != nil {
			return oasis.CodeResult{}, fmt.Errorf("upload input file %q: %w", f.Name, err)
		}
	}

	// Select runtime, prelude, postlude, and file extension.
	var prelude, postlude, ext string
	runtime := req.Runtime
	if runtime == "" {
		runtime = "python"
	}
	switch runtime {
	case "node":
		prelude = osJsPrelude
		postlude = osJsPostlude
		ext = ".js"
	default: // "python"
		prelude = osPyPrelude
		postlude = osPyPostlude
		ext = ".py"
	}

	// Build the full script.
	script := prelude + "\n" + req.Code + "\n" + postlude

	// Upload script to /tmp/oasis_{id}.{ext}.
	scriptPath := fmt.Sprintf("/tmp/oasis_%s%s", executionID, ext)
	if err := r.api.uploadFile(ctx, sb, osFileMetadata{Path: scriptPath}, []byte(script)); err != nil {
		return oasis.CodeResult{}, fmt.Errorf("upload script: %w", err)
	}

	// Register callback channel and start drain goroutine.
	stopCh := make(chan struct{})
	dispatchCh := r.server.register(executionID)
	defer close(stopCh)
	defer r.server.deregister(executionID)
	go drainDispatch(ctx, dispatchCh, dispatch, stopCh)

	// Build the command to execute.
	var command string
	switch runtime {
	case "node":
		command = "node " + scriptPath
	default:
		command = "python3 " + scriptPath
	}

	// Execute the command in the sandbox.
	envs := map[string]string{
		"_SANDBOX_CALLBACK_URL":  cbURL,
		"_SANDBOX_EXECUTION_ID":  executionID,
		"_SANDBOX_WORKSPACE":     "/workspace",
	}

	cmdReq := osCommandRequest{
		Command: command,
		Cwd:     "/workspace",
		Timeout: int(timeout.Milliseconds()),
		Envs:    envs,
	}

	resp, err := r.api.executeCommand(ctx, sb, cmdReq)
	if err != nil {
		return oasis.CodeResult{}, fmt.Errorf("execute command: %w", err)
	}
	defer resp.Body.Close()

	// Parse SSE stream — collect stdout, stderr, errors.
	var (
		stdoutBuf strings.Builder
		stderrBuf strings.Builder
		execError string
		exitCode  int
	)

	err = parseSSEStream(ctx, resp.Body, func(e osSSEEvent) {
		switch e.Type {
		case "stdout":
			stdoutBuf.WriteString(e.Text)
		case "stderr":
			stderrBuf.WriteString(e.Text)
		case "error":
			exitCode = 1
			if e.Error != nil {
				execError = e.Error.Ename + ": " + e.Error.Evalue
				if len(e.Error.Traceback) > 0 {
					stderrBuf.WriteString(strings.Join(e.Error.Traceback, "\n"))
				}
			}
		case "execution_complete":
			// No-op — indicates the command finished.
		}
	})
	if err != nil {
		return oasis.CodeResult{}, fmt.Errorf("parse SSE stream: %w", err)
	}

	// Parse protocol messages from stdout (result, result_files).
	var (
		resultJSON  string
		resultFiles []string
	)
	for _, line := range strings.Split(stdoutBuf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			Type  string   `json:"type"`
			Data  any      `json:"data"`
			Files []string `json:"files"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
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

	// Build CodeResult.
	result := oasis.CodeResult{
		Output:   resultJSON,
		Logs:     stderrBuf.String(),
		ExitCode: exitCode,
		Error:    execError,
	}

	// Download output files.
	for _, fpath := range resultFiles {
		remotePath := "/workspace/" + fpath
		data, err := r.api.downloadFile(ctx, sb, remotePath)
		if err != nil {
			continue // skip files we can't download
		}
		if r.cfg.maxFileSize > 0 && int64(len(data)) > r.cfg.maxFileSize {
			// Degrade: include metadata but not data.
			result.Files = append(result.Files, oasis.CodeFile{
				Name: filepath.Base(fpath),
				MIME: osDetectMIME(fpath, nil),
			})
			continue
		}
		result.Files = append(result.Files, oasis.CodeFile{
			Name: filepath.Base(fpath),
			MIME: osDetectMIME(fpath, data),
			Data: data,
		})
	}

	// Cleanup ephemeral sandbox.
	if ephemeral {
		r.mu.Lock()
		delete(r.sandboxes, sessionKey)
		r.mu.Unlock()
		go r.api.deleteSandbox(context.Background(), sb.id) //nolint:errcheck
	}

	return result, nil
}

// osDetectMIME returns a MIME type for the given filename and data.
// Uses extension-based detection with http.DetectContentType as fallback.
func osDetectMIME(name string, data []byte) string {
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
	if len(data) == 0 {
		return "application/octet-stream"
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	return http.DetectContentType(sniff)
}
