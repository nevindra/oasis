package ixd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// browserProxy proxies browser requests to the Pinchtab bridge subprocess.
type browserProxy struct {
	pt     *pinchtab
	client *http.Client
}

func newBrowserProxy(pt *pinchtab) *browserProxy {
	return &browserProxy{pt: pt, client: &http.Client{}}
}

// checkAvailable returns HTTP 501 if Pinchtab is not available.
func (b *browserProxy) checkAvailable(w http.ResponseWriter) bool {
	if !b.pt.isAvailable() {
		writeError(w, http.StatusNotImplemented,
			"browser not available: use oasis-ix-browser image")
		return false
	}
	return true
}

// handleNavigate proxies POST /v1/browser/navigate to Pinchtab POST /navigate.
func (b *browserProxy) handleNavigate(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, _ := json.Marshal(map[string]string{"url": req.URL})
	resp, err := b.forward(r.Context(), http.MethodPost, "/navigate", body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handleAction proxies POST /v1/browser/action to Pinchtab POST /action.
func (b *browserProxy) handleAction(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	var req struct {
		Kind      string `json:"kind"`
		Ref       string `json:"ref"`
		X         int    `json:"x"`
		Y         int    `json:"y"`
		Text      string `json:"text"`
		Key       string `json:"key"`
		Direction string `json:"direction"`
		Value     string `json:"value"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload := map[string]any{"kind": req.Kind}
	if req.Ref != "" {
		payload["ref"] = req.Ref
	}
	if req.X != 0 || req.Y != 0 {
		payload["x"] = req.X
		payload["y"] = req.Y
	}
	if req.Text != "" {
		payload["text"] = req.Text
	}
	if req.Key != "" {
		payload["key"] = req.Key
	}
	if req.Direction != "" {
		payload["direction"] = req.Direction
	}
	if req.Value != "" {
		payload["value"] = req.Value
	}

	body, _ := json.Marshal(payload)
	resp, err := b.forward(r.Context(), http.MethodPost, "/action", body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handleEvaluate proxies POST /v1/browser/evaluate to Pinchtab POST /evaluate.
func (b *browserProxy) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	var req struct {
		Expression string `json:"expression"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, _ := json.Marshal(map[string]string{"expression": req.Expression})
	resp, err := b.forward(r.Context(), http.MethodPost, "/evaluate", body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handleFind proxies POST /v1/browser/find to Pinchtab POST /find.
func (b *browserProxy) handleFind(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, _ := json.Marshal(map[string]string{"query": req.Query})
	resp, err := b.forward(r.Context(), http.MethodPost, "/find", body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handleScreenshot proxies GET /v1/browser/screenshot to Pinchtab GET /screenshot.
func (b *browserProxy) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	resp, err := b.forward(r.Context(), http.MethodGet, "/screenshot?raw=true", nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// handleSnapshot proxies GET /v1/browser/snapshot to Pinchtab GET /snapshot.
func (b *browserProxy) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	q := url.Values{}
	if v := r.URL.Query().Get("filter"); v != "" {
		q.Set("filter", v)
	}
	if v := r.URL.Query().Get("selector"); v != "" {
		q.Set("selector", v)
	}
	if v := r.URL.Query().Get("depth"); v != "" {
		q.Set("depth", v)
	}

	path := "/snapshot"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := b.forward(r.Context(), http.MethodGet, path, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handleText proxies GET /v1/browser/text to Pinchtab GET /text.
func (b *browserProxy) handleText(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	q := url.Values{}
	if r.URL.Query().Get("raw") == "true" {
		q.Set("mode", "raw")
	}
	if v := r.URL.Query().Get("maxChars"); v != "" {
		q.Set("maxChars", v)
	}

	path := "/text"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := b.forward(r.Context(), http.MethodGet, path, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handlePDF proxies GET /v1/browser/pdf to Pinchtab GET /pdf.
func (b *browserProxy) handlePDF(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	resp, err := b.forward(r.Context(), http.MethodGet, "/pdf", nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// forward sends an HTTP request to the Pinchtab bridge and returns the response.
func (b *browserProxy) forward(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, b.pt.baseURL()+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+internalToken)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pinchtab %s %s: %w", method, path, err)
	}
	return resp, nil
}

// copyResponse copies a Pinchtab response to the ix daemon response writer.
func copyResponse(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
