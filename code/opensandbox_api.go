package code

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// --- OpenSandbox API wire types ---

type osCreateRequest struct {
	Image          osImage            `json:"image"`
	Timeout        *int               `json:"timeout,omitempty"`
	ResourceLimits osResourceLimits   `json:"resource_limits"`
	Env            map[string]string  `json:"env,omitempty"`
	Entrypoint     []string           `json:"entrypoint,omitempty"`
}

type osImage struct {
	URI string `json:"uri"`
}

type osResourceLimits struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

type osSandbox struct {
	ID     string   `json:"id"`
	Status osStatus `json:"status"`
}

type osStatus struct {
	State string `json:"state"`
}

type osEndpoint struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers"`
}

type osAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *osAPIError) Error() string {
	return fmt.Sprintf("opensandbox API error %s: %s", e.Code, e.Message)
}

type osCommandRequest struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Timeout int               `json:"timeout"` // milliseconds
	Envs    map[string]string `json:"envs,omitempty"`
}

type osFileMetadata struct {
	Path string `json:"path"`
}

type osSSEEvent struct {
	Type          string      `json:"type"`
	Text          string      `json:"text,omitempty"`
	Timestamp     int64       `json:"timestamp,omitempty"`
	ExecutionTime int         `json:"execution_time,omitempty"`
	Error         *osSSEError `json:"error,omitempty"`
}

type osSSEError struct {
	Ename     string   `json:"ename"`
	Evalue    string   `json:"evalue"`
	Traceback []string `json:"traceback"`
}

// --- SSE stream parser ---

// parseSSEStream reads raw JSON lines from r, separated by blank lines.
// Pings are silently skipped. For each non-ping event, handler is called.
func parseSSEStream(ctx context.Context, r io.Reader, handler func(osSSEEvent)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB buffer

	for scanner.Scan() {
		// Check for context cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue // skip blank lines (SSE separators)
		}

		var event osSSEEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue // skip unparseable lines
		}

		// Skip ping events.
		if event.Type == "ping" {
			continue
		}

		handler(event)
	}

	return scanner.Err()
}

// --- API client ---

// sandboxInstance holds the connection details for a running sandbox.
type sandboxInstance struct {
	id      string
	execd   string            // base URL e.g. "http://10.0.0.5:44772"
	headers map[string]string // routing headers from endpoint
}

// osAPI is a client for the OpenSandbox lifecycle and execd APIs.
type osAPI struct {
	serverURL  string
	apiKey     string
	execdToken string
	client     *http.Client
}

func newOSAPI(serverURL, apiKey, execdToken string) *osAPI {
	return &osAPI{
		serverURL:  strings.TrimRight(serverURL, "/"),
		apiKey:     apiKey,
		execdToken: execdToken,
		client:     &http.Client{Timeout: 30 * time.Second},
	}
}

// createSandbox creates a new sandbox via POST /v1/sandboxes.
func (a *osAPI) createSandbox(ctx context.Context, req osCreateRequest) (osSandbox, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return osSandbox{}, fmt.Errorf("marshal create request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.serverURL+"/v1/sandboxes", bytes.NewReader(body))
	if err != nil {
		return osSandbox{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("OPEN-SANDBOX-API-KEY", a.apiKey)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return osSandbox{}, fmt.Errorf("create sandbox: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return osSandbox{}, fmt.Errorf("read create response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr osAPIError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Message != "" {
			return osSandbox{}, &apiErr
		}
		return osSandbox{}, fmt.Errorf("create sandbox: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var sandbox osSandbox
	if err := json.Unmarshal(respBody, &sandbox); err != nil {
		return osSandbox{}, fmt.Errorf("parse create response: %w", err)
	}
	return sandbox, nil
}

// deleteSandbox deletes a sandbox via DELETE /v1/sandboxes/{id}.
func (a *osAPI) deleteSandbox(ctx context.Context, id string) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, a.serverURL+"/v1/sandboxes/"+id, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("OPEN-SANDBOX-API-KEY", a.apiKey)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("delete sandbox %s: %w", id, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("delete sandbox %s: HTTP %d", id, resp.StatusCode)
	}
	return nil
}

// getEndpoint retrieves the execd endpoint for a sandbox port via
// GET /v1/sandboxes/{id}/endpoints/{port}.
func (a *osAPI) getEndpoint(ctx context.Context, id string, port int) (osEndpoint, error) {
	url := fmt.Sprintf("%s/v1/sandboxes/%s/endpoints/%d", a.serverURL, id, port)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return osEndpoint{}, err
	}
	httpReq.Header.Set("OPEN-SANDBOX-API-KEY", a.apiKey)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return osEndpoint{}, fmt.Errorf("get endpoint: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return osEndpoint{}, fmt.Errorf("read endpoint response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr osAPIError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Message != "" {
			return osEndpoint{}, &apiErr
		}
		return osEndpoint{}, fmt.Errorf("get endpoint: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var ep osEndpoint
	if err := json.Unmarshal(respBody, &ep); err != nil {
		return osEndpoint{}, fmt.Errorf("parse endpoint response: %w", err)
	}
	return ep, nil
}

// waitReady polls the sandbox execd /ping endpoint until it responds 200.
func (a *osAPI) waitReady(ctx context.Context, sb *sandboxInstance) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox %s not ready: %w", sb.id, ctx.Err())
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, sb.execd+"/ping", nil)
			if err != nil {
				continue
			}
			a.applyExecdHeaders(req, sb)

			resp, err := a.client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
}

// executeCommand sends a command to the sandbox execd via POST /command.
// Returns the raw response for SSE streaming. The caller must close the body.
// Uses a separate http.Client with no timeout for long-lived SSE connections.
func (a *osAPI) executeCommand(ctx context.Context, sb *sandboxInstance, cmd osCommandRequest) (*http.Response, error) {
	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal command: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sb.execd+"/command", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.applyExecdHeaders(httpReq, sb)

	// Use a client with no timeout for long-lived SSE streams.
	sseClient := &http.Client{}
	resp, err := sseClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute command: %w", err)
	}

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, fmt.Errorf("execute command: HTTP %d: %s", resp.StatusCode, respBody)
	}

	return resp, nil
}

// uploadFile uploads a file to the sandbox via POST /files/upload (multipart).
func (a *osAPI) uploadFile(ctx context.Context, sb *sandboxInstance, meta osFileMetadata, data []byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Write metadata part.
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal file metadata: %w", err)
	}
	if err := w.WriteField("metadata", string(metaJSON)); err != nil {
		return fmt.Errorf("write metadata field: %w", err)
	}

	// Write file data part.
	fw, err := w.CreateFormFile("file", meta.Path)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return fmt.Errorf("write file data: %w", err)
	}
	w.Close()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sb.execd+"/files/upload", &buf)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	a.applyExecdHeaders(httpReq, sb)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("upload file: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload file %s: HTTP %d", meta.Path, resp.StatusCode)
	}
	return nil
}

// downloadFile downloads a file from the sandbox via GET /files/download?path=...
func (a *osAPI) downloadFile(ctx context.Context, sb *sandboxInstance, path string) ([]byte, error) {
	url := sb.execd + "/files/download?path=" + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	a.applyExecdHeaders(httpReq, sb)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("download file %s: HTTP %d", path, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB limit
	if err != nil {
		return nil, fmt.Errorf("read download: %w", err)
	}
	return data, nil
}

// applyExecdHeaders sets the execd access token and routing headers on a request.
func (a *osAPI) applyExecdHeaders(req *http.Request, sb *sandboxInstance) {
	if a.execdToken != "" {
		req.Header.Set("X-EXECD-ACCESS-TOKEN", a.execdToken)
	}
	for k, v := range sb.headers {
		req.Header.Set(k, v)
	}
}
