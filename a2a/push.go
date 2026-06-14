package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// defaultPushTimeout bounds a single webhook delivery. A tight timeout ensures
// a dead or slow webhook cannot wedge task settlement — push is best-effort by
// spec; the client can always poll GetTask for the authoritative state.
const defaultPushTimeout = 10 * time.Second

// newDefaultPushClient builds the *http.Client NewServer uses for webhook
// delivery when the caller does not inject one via WithPushHTTPClient.
func newDefaultPushClient() *http.Client { return &http.Client{Timeout: defaultPushTimeout} }

// deliverPush POSTs the settled task to the registered webhook. All failures
// are logged and never propagated: push delivery is fire-and-forget by the A2A
// spec — a missed webhook is not a data-loss event because the task state is
// durably stored and readable via GetTask.
//
// ctx should be s.baseCtx (the server-lifetime context) rather than the
// run's context. Why: by the time settle() calls deliverPush, the run's
// runCtx may already be canceled (e.g. for a CancelTask flow). We still
// want to attempt delivery for canceled tasks — the webhook notifying the
// caller that the task was canceled is exactly correct behavior. Only
// Server.Close() (which cancels s.baseCtx) should suppress delivery.
func (s *Server) deliverPush(ctx context.Context, cfg *PushNotificationConfig, task Task) {
	body, err := json.Marshal(StreamResponse{Task: &task})
	if err != nil {
		slog.Error("a2a: push: marshal payload failed", "task", task.ID, "url", cfg.URL, "err", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		slog.Error("a2a: push: build request failed", "task", task.ID, "url", cfg.URL, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		// Why: token echo lets the webhook receiver authenticate the caller
		// without a shared secret per the A2A push spec §4.3.
		req.Header.Set("X-A2A-Notification-Token", cfg.Token)
	}

	resp, err := s.pushClient.Do(req)
	if err != nil {
		slog.Error("a2a: push: delivery failed", "task", task.ID, "url", cfg.URL, "err", err)
		return
	}
	// Why: always drain + close the body even on error so the HTTP client can
	// reuse the underlying connection (net/http requirement).
	defer resp.Body.Close()
	// Discard the body; we only care that the webhook received our POST.
	// A bounded discard (4 KiB) prevents a malicious webhook from stalling us.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("a2a: push: webhook returned non-2xx", "task", task.ID, "url", cfg.URL, "status", resp.StatusCode)
	}
}

// taskPushConfig is the TaskPushNotificationConfig wire object: the config
// fields flattened beside the owning task ID (A2A v1.0 proto shape — the
// config is the request/response object itself, not a nested field).
// All four methods share this shape: Create uses all fields; Get/Delete use
// {taskId, id}; List uses {taskId} — each is a valid subset of the flat shape.
type taskPushConfig struct {
	TaskID string `json:"taskId"`
	PushNotificationConfig
}

// handlePushConfig implements the four TaskPushNotificationConfig methods
// (Create, Get, List, Delete). It is dispatched by serveJSONRPC for all four
// method constants and uses method to select the operation.
//
// The wire params shape is the flat TaskPushNotificationConfig object (A2A
// v1.0 proto shape): config fields sit beside taskId at the top level. All
// four methods share one decode of taskPushConfig since {taskId,id} is a
// valid subset of the full flat shape.
//
// v1 supports one config per task (the spec allows N; List returns 0 or 1).
// This is a deliberate simplification: most callers register one webhook and
// never update it; adding a collection adds store complexity with no real
// benefit pre-v1. Revisit if multi-config is needed post-v1.
func (s *Server) handlePushConfig(ctx context.Context, method string, p json.RawMessage) (any, *rpcError) {
	if !s.opts.pushEnabled {
		return nil, &rpcError{Code: codePushNotSupported, Message: ErrPushNotSupported.Error()}
	}

	var params taskPushConfig
	if err := json.Unmarshal(p, &params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if params.TaskID == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: taskId required"}
	}

	entry, err := s.store.Get(ctx, params.TaskID)
	if err != nil {
		return nil, &rpcError{Code: codeTaskNotFound, Message: err.Error()}
	}

	// Read-only methods never mutate entry, so they hold the lock for the
	// full switch. Mutating methods (Create/Delete) release the lock before
	// calling store.Save, mirroring settle()'s lock discipline: I/O must not
	// be performed while holding entry.mu.
	switch method {
	case methodCreatePushConfig:
		if params.URL == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: non-empty url required"}
		}
		cfg := params.PushNotificationConfig
		if cfg.ID == "" {
			cfg.ID = newID()
		}
		entry.mu.Lock()
		entry.push = &cfg
		entry.mu.Unlock()
		// Persist outside the lock so custom store implementations can do I/O
		// without holding entry.mu. Log-and-continue: in-process state is
		// authoritative for v1; a persistence failure is non-fatal for the caller.
		if err := s.store.Save(ctx, entry); err != nil {
			slog.Error("a2a: push config create: persist failed", "task", params.TaskID, "err", err)
		}
		return taskPushConfig{TaskID: params.TaskID, PushNotificationConfig: cfg}, nil

	case methodGetPushConfig:
		entry.mu.Lock()
		defer entry.mu.Unlock()
		if entry.push == nil {
			// Why: reuse codeTaskNotFound — the spec defines no config-not-found
			// code; config absence is indistinguishable from task absence to the
			// caller (they both indicate "nothing to get").
			return nil, &rpcError{Code: codeTaskNotFound, Message: "no push config for task " + params.TaskID}
		}
		return taskPushConfig{TaskID: params.TaskID, PushNotificationConfig: *entry.push}, nil

	case methodListPushConfigs:
		entry.mu.Lock()
		defer entry.mu.Unlock()
		// Why: always return a non-nil slice so the caller gets [] not null on
		// the wire — consistent with the List<X> contract in ENGINEERING.md.
		if entry.push == nil {
			return []taskPushConfig{}, nil
		}
		return []taskPushConfig{{TaskID: params.TaskID, PushNotificationConfig: *entry.push}}, nil

	case methodDeletePushConfig:
		entry.mu.Lock()
		entry.push = nil
		entry.mu.Unlock()
		// Persist outside the lock; same reasoning as Create above.
		if err := s.store.Save(ctx, entry); err != nil {
			slog.Error("a2a: push config delete: persist failed", "task", params.TaskID, "err", err)
		}
		return struct{}{}, nil
	}

	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}
}
