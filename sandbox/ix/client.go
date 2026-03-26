package ix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
)

// ixClient is a thin HTTP client for the ix daemon REST + SSE API.
type ixClient struct {
	baseURL string
	http    *http.Client
}

func newClient(baseURL string, httpClient *http.Client) *ixClient {
	return &ixClient{baseURL: baseURL, http: httpClient}
}

// post sends a JSON POST request and decodes the response into dst.
func (c *ixClient) post(ctx context.Context, path string, body any, dst any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("request %s: HTTP %d: %s", path, resp.StatusCode, respBody)
	}
	if dst != nil {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return fmt.Errorf("decode response %s: %w", path, err)
		}
	}
	return nil
}

// postSSE sends a JSON POST with Accept: text/event-stream and returns an SSE reader.
func (c *ixClient) postSSE(ctx context.Context, path string, body any) (*sseReader, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("request %s: HTTP %d: %s", path, resp.StatusCode, respBody)
	}

	return newSSEReader(resp.Body), nil
}

// getRaw sends a GET request and returns the raw response body.
// The caller must close the returned reader.
func (c *ixClient) getRaw(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", path, err)
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("request %s: HTTP %d", path, resp.StatusCode)
	}
	return resp.Body, nil
}

// getJSON sends a GET request and decodes the JSON response into dst.
func (c *ixClient) getJSON(ctx context.Context, path string, dst any) error {
	rc, err := c.getRaw(ctx, path)
	if err != nil {
		return err
	}
	defer rc.Close()
	return json.NewDecoder(rc).Decode(dst)
}

// upload sends a multipart file upload.
func (c *ixClient) upload(ctx context.Context, path, filePath string, data io.Reader) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("path", filePath)
	fw, err := w.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(fw, data); err != nil {
		return fmt.Errorf("copy file data: %w", err)
	}
	w.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("upload %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

// sseReader reads Server-Sent Events from an HTTP response body.
type sseReader struct {
	scanner *bufio.Scanner
	event   string
	data    string
	err     error
	body    io.ReadCloser
}

func newSSEReader(body io.ReadCloser) *sseReader {
	return &sseReader{
		scanner: bufio.NewScanner(body),
		body:    body,
	}
}

// Next advances to the next complete event. Returns false at EOF or error.
func (r *sseReader) Next() bool {
	// Parse SSE format:
	// event: <type>\n
	// data: <json>\n
	// \n (empty line = end of event)
	// Lines starting with : are comments (ping), skip them
	r.event = ""
	r.data = ""
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			// Empty line = end of event
			if r.event != "" {
				return true
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment (ping), skip
		}
		if strings.HasPrefix(line, "event: ") {
			r.event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			r.data = strings.TrimPrefix(line, "data: ")
		}
	}
	r.err = r.scanner.Err()
	return false
}

// Event returns the event type of the current event.
func (r *sseReader) Event() string { return r.event }

// Data returns the data payload of the current event.
func (r *sseReader) Data() string { return r.data }

// Err returns any error encountered during scanning.
func (r *sseReader) Err() error { return r.err }

// Close closes the underlying response body.
func (r *sseReader) Close() error { return r.body.Close() }
