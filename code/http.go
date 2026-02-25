package code

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	oasis "github.com/nevindra/oasis"
)

// HTTPRunner executes code by POSTing to a remote sandbox service.
// It implements oasis.CodeRunner.
//
// The sandbox communicates tool calls back via HTTP to a callback server
// managed by HTTPRunner. On first Run(), the callback server starts
// automatically unless WithCallbackExternal was configured.
type HTTPRunner struct {
	cfg       runnerConfig
	server    *callbackServer
	startOnce sync.Once
	startErr  error
	client    *http.Client
}

// compile-time check
var _ oasis.CodeRunner = (*HTTPRunner)(nil)

// NewHTTPRunner creates an HTTPRunner that POSTs code to the sandbox
// at sandboxURL (e.g. "http://sandbox:9000").
func NewHTTPRunner(sandboxURL string, opts ...Option) *HTTPRunner {
	cfg := defaultConfig()
	cfg.sandboxURL = strings.TrimRight(sandboxURL, "/")
	for _, o := range opts {
		o(&cfg)
	}

	return &HTTPRunner{
		cfg:    cfg,
		server: newCallbackServer(),
		client: &http.Client{},
	}
}

// Handler returns the http.Handler for the /_oasis/dispatch endpoint.
// Mount this on your own mux when using WithCallbackExternal:
//
//	mux.Handle("/_oasis/dispatch", runner.Handler())
func (r *HTTPRunner) Handler() http.Handler {
	return r.server.Handler()
}

// Close shuts down the auto-started callback server.
// No-op when WithCallbackExternal is set.
func (r *HTTPRunner) Close() error {
	return r.server.Close()
}

// Run executes code via the sandbox HTTP service.
// Implements oasis.CodeRunner.
func (r *HTTPRunner) Run(ctx context.Context, req oasis.CodeRequest, dispatch oasis.DispatchFunc) (oasis.CodeResult, error) {
	if err := r.ensureStarted(); err != nil {
		return oasis.CodeResult{}, err
	}

	// Determine timeout.
	timeout := r.cfg.timeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Generate execution ID for callback correlation.
	executionID := oasis.NewID()

	// Build callback URL.
	callbackURL := r.callbackURL()

	// Build sandbox request.
	execReq := sandboxExecRequest{
		ExecutionID: executionID,
		CallbackURL: callbackURL,
		Code:        req.Code,
		Runtime:     req.Runtime,
		SessionID:   req.SessionID,
		TimeoutSecs: int(timeout.Seconds()),
	}

	// Convert input files: Data → base64 for wire format.
	for _, f := range req.Files {
		wf := wireFile{Name: f.Name, MIME: f.MIME, URL: f.URL}
		if len(f.Data) > 0 {
			wf.Data = base64.StdEncoding.EncodeToString(f.Data)
		}
		execReq.Files = append(execReq.Files, wf)
	}

	// Start dispatch drain goroutine — processes tool callbacks from sandbox.
	// Defer order matters (LIFO): stopCh must close AFTER deregister removes
	// the mapping, so no new envelopes arrive after drainDispatch exits.
	stopCh := make(chan struct{})
	dispatchCh := r.server.register(executionID)
	defer close(stopCh)
	defer r.server.deregister(executionID)
	go r.drainDispatch(ctx, dispatchCh, dispatch, stopCh)

	// POST to sandbox /execute with retry.
	resp, err := r.doExecute(ctx, execReq)
	if err != nil {
		return oasis.CodeResult{}, fmt.Errorf("sandbox execution failed: %w", err)
	}

	// Map response to CodeResult.
	result := oasis.CodeResult{
		Output:   resp.Output,
		Logs:     resp.Logs,
		ExitCode: resp.ExitCode,
		Error:    resp.Error,
	}

	// Decode output files.
	for _, f := range resp.Files {
		cf := oasis.CodeFile{Name: f.Name, MIME: f.MIME, URL: f.URL}
		if f.Data != "" {
			decoded, err := base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				continue // skip malformed files
			}
			if r.cfg.maxFileSize > 0 && int64(len(decoded)) > r.cfg.maxFileSize {
				// Degrade: include metadata but not data.
				result.Files = append(result.Files, oasis.CodeFile{Name: f.Name, MIME: f.MIME})
				continue
			}
			cf.Data = decoded
		}
		result.Files = append(result.Files, cf)
	}

	return result, nil
}

// ensureStarted lazily starts the callback server on first Run().
func (r *HTTPRunner) ensureStarted() error {
	r.startOnce.Do(func() {
		if r.cfg.callbackExtAddr != "" {
			// External mount — user handles the HTTP server.
			return
		}
		r.startErr = r.server.Start(r.cfg.callbackAddr)
	})
	return r.startErr
}

// callbackURL returns the full URL the sandbox should POST tool calls to.
func (r *HTTPRunner) callbackURL() string {
	if r.cfg.callbackExtAddr != "" {
		return strings.TrimRight(r.cfg.callbackExtAddr, "/") + callbackPath
	}
	return "http://" + r.server.Addr() + callbackPath
}

// drainDispatch processes tool call envelopes from the dispatch channel.
// Each envelope is dispatched concurrently; the result is sent back via replyCh.
// On exit, drains any remaining envelopes with error replies to prevent
// handleDispatch goroutines from blocking on replyCh indefinitely.
func (r *HTTPRunner) drainDispatch(ctx context.Context, dispatchCh chan dispatchEnvelope, dispatch oasis.DispatchFunc, stopCh chan struct{}) {
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case env, ok := <-dispatchCh:
			if !ok {
				return
			}
			wg.Add(1)
			go func(env dispatchEnvelope) {
				defer wg.Done()
				dr := dispatch(ctx, env.call)
				env.replyCh <- dispatchReply{
					content: dr.Content,
					isError: dr.IsError,
				}
			}(env)
		case <-stopCh:
			// Drain any remaining envelopes and reply with errors
			// so handleDispatch goroutines don't block on replyCh.
			for {
				select {
				case env := <-dispatchCh:
					env.replyCh <- dispatchReply{
						content: "execution completed",
						isError: true,
					}
				default:
					return
				}
			}
		case <-ctx.Done():
			// Same drain on context cancellation.
			for {
				select {
				case env := <-dispatchCh:
					env.replyCh <- dispatchReply{
						content: "execution cancelled",
						isError: true,
					}
				default:
					return
				}
			}
		}
	}
}

// --- sandbox wire types ---

type sandboxExecRequest struct {
	ExecutionID string     `json:"execution_id"`
	CallbackURL string     `json:"callback_url"`
	Code        string     `json:"code"`
	Runtime     string     `json:"runtime"`
	SessionID   string     `json:"session_id,omitempty"`
	TimeoutSecs int        `json:"timeout"`
	Files       []wireFile `json:"files,omitempty"`
}

type sandboxExecResponse struct {
	Output   string     `json:"output"`
	Logs     string     `json:"logs"`
	ExitCode int        `json:"exit_code"`
	Error    string     `json:"error,omitempty"`
	Files    []wireFile `json:"files,omitempty"`
}

// wireFile is the JSON wire format for files (base64 encoded).
type wireFile struct {
	Name string `json:"name"`
	MIME string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"` // base64
	URL  string `json:"url,omitempty"`
}

// doExecute POSTs the execution request to the sandbox with retry logic.
func (r *HTTPRunner) doExecute(ctx context.Context, execReq sandboxExecRequest) (sandboxExecResponse, error) {
	body, err := json.Marshal(execReq)
	if err != nil {
		return sandboxExecResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	delay := r.cfg.retryDelay

	for attempt := 0; attempt < r.cfg.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(delay):
				delay *= 2
			case <-ctx.Done():
				return sandboxExecResponse{}, ctx.Err()
			}
		}

		resp, err := r.doOnce(ctx, body)
		if err == nil {
			return resp, nil
		}
		if !isTransient(err) {
			return sandboxExecResponse{}, err
		}
		lastErr = err
	}

	return sandboxExecResponse{}, fmt.Errorf("sandbox unreachable after %d attempts: %w", r.cfg.maxRetries, lastErr)
}

// doOnce performs a single POST to /execute.
func (r *HTTPRunner) doOnce(ctx context.Context, body []byte) (sandboxExecResponse, error) {
	url := r.cfg.sandboxURL + "/execute"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return sandboxExecResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return sandboxExecResponse{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB limit
	if err != nil {
		return sandboxExecResponse{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 500 {
		return sandboxExecResponse{}, &serverError{code: resp.StatusCode, body: string(respBody)}
	}
	if resp.StatusCode != http.StatusOK {
		return sandboxExecResponse{}, fmt.Errorf("sandbox returned %d: %s", resp.StatusCode, respBody)
	}

	var result sandboxExecResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return sandboxExecResponse{}, fmt.Errorf("parse response: %w", err)
	}
	return result, nil
}

// serverError represents a 5xx response from the sandbox.
type serverError struct {
	code int
	body string
}

func (e *serverError) Error() string {
	return fmt.Sprintf("sandbox returned %d: %s", e.code, e.body)
}

// isTransient reports whether err is a transient network/server error
// that should be retried.
func isTransient(err error) bool {
	if _, ok := err.(*serverError); ok {
		return true
	}
	// net/http wraps network errors — check for timeout.
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// Connection refused, reset, etc.
	errMsg := err.Error()
	return strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "EOF")
}
