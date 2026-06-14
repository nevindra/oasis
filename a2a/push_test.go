package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/oasis/a2a/a2atest"
)

// TestPushDelivery verifies end-to-end webhook delivery: a non-blocking send
// with a push config causes the server to POST the settled task to the
// registered URL once the background run completes.
func TestPushDelivery(t *testing.T) {
	var got atomic.Pointer[StreamResponse]
	var gotToken atomic.Pointer[string]
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-A2A-Notification-Token")
		gotToken.Store(&tok)
		var sr StreamResponse
		json.NewDecoder(r.Body).Decode(&sr)
		got.Store(&sr)
	}))
	defer hook.Close()

	srv := NewServer(a2atest.NewEchoAgent("echo", "echoes"), WithPushNotifications())
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	task := sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("hi")}},
		Configuration: &SendConfiguration{
			Blocking:               NonBlockingPtr(),
			PushNotificationConfig: &PushNotificationConfig{URL: hook.URL, Token: "tok-1"},
		},
	}))
	// A non-blocking send returns the task's current state immediately. That is
	// normally Working, but a fast agent (echo) may already have settled by the
	// time the response is read — Completed is equally valid. The point is the
	// call did not block on the terminal state; blocking-vs-non-blocking
	// semantics are covered deterministically by TestSendConfigurationBlockingDefault
	// and TestMessageSendZeroConfigBlocks.
	if task.Status.State != TaskStateWorking && task.Status.State != TaskStateCompleted {
		t.Fatalf("non-blocking send must return working or completed, got %s", task.Status.State)
	}

	// Webhook fires when the background run settles.
	deadline := time.After(2 * time.Second)
	for got.Load() == nil {
		select {
		case <-deadline:
			t.Fatal("webhook never called")
		case <-time.After(10 * time.Millisecond):
		}
	}
	sr := got.Load()
	if sr.Task == nil || sr.Task.Status.State != TaskStateCompleted {
		t.Errorf("push payload = %+v", sr)
	}
	if tok := gotToken.Load(); tok == nil || *tok != "tok-1" {
		t.Error("push must echo the client token for webhook authentication")
	}
}

// recordingTransport is an http.RoundTripper that flags itself as used and
// delegates to a real transport, so a test can prove the server delivered a
// webhook through the injected client rather than a package default.
type recordingTransport struct {
	used atomic.Bool
	next http.RoundTripper
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.used.Store(true)
	return rt.next.RoundTrip(req)
}

// TestPushUsesInjectedClient verifies WithPushHTTPClient: the server delivers
// webhooks through the caller-supplied *http.Client, not a package global. The
// injected client wraps a recording transport; the test asserts that transport
// saw the delivery request.
func TestPushUsesInjectedClient(t *testing.T) {
	var got atomic.Pointer[StreamResponse]
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sr StreamResponse
		json.NewDecoder(r.Body).Decode(&sr)
		got.Store(&sr)
	}))
	defer hook.Close()

	rt := &recordingTransport{next: http.DefaultTransport}
	injected := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	srv := NewServer(
		a2atest.NewEchoAgent("echo", "echoes"),
		WithPushNotifications(),
		WithPushHTTPClient(injected),
	)
	defer srv.Close()
	if srv.pushClient != injected {
		t.Fatal("server must use the injected push client")
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("hi")}},
		Configuration: &SendConfiguration{
			Blocking:               NonBlockingPtr(),
			PushNotificationConfig: &PushNotificationConfig{URL: hook.URL},
		},
	}))

	deadline := time.After(2 * time.Second)
	for got.Load() == nil {
		select {
		case <-deadline:
			t.Fatal("webhook never called")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !rt.used.Load() {
		t.Error("push delivery did not go through the injected client's transport")
	}
}

// TestPushRejectedWhenDisabled verifies that a non-blocking send with a push
// config is rejected with codePushNotSupported when the server was not
// constructed with WithPushNotifications().
func TestPushRejectedWhenDisabled(t *testing.T) {
	srv := NewServer(a2atest.NewEchoAgent("echo", "echoes")) // no WithPushNotifications
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp := rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("hi")}},
		Configuration: &SendConfiguration{
			Blocking:               NonBlockingPtr(), // explicit non-blocking opt-out
			PushNotificationConfig: &PushNotificationConfig{URL: "http://x"},
		},
	})
	if resp.Error == nil || resp.Error.Code != codePushNotSupported {
		t.Errorf("want %d, got %+v", codePushNotSupported, resp.Error)
	}
}

// TestPushConfigCRUD exercises all four TaskPushNotificationConfig JSON-RPC
// methods against a push-enabled server: Create → Get → List → Delete → List.
// Also verifies that each method returns codeTaskNotFound for an unknown task
// and codePushNotSupported when push is disabled on the server.
func TestPushConfigCRUD(t *testing.T) {
	// Create a push-enabled server and complete a task to get a real task ID.
	srv := NewServer(a2atest.NewEchoAgent("echo", "echoes"), WithPushNotifications())
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Complete a task via blocking send so we have a known task ID.
	task := sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("hello")}},
	}))
	if task.Status.State != TaskStateCompleted {
		t.Fatalf("setup task must complete, got %s", task.Status.State)
	}
	taskID := task.ID

	cfg := taskPushConfig{TaskID: taskID, PushNotificationConfig: PushNotificationConfig{URL: "https://example.com/hook", Token: "t1"}}

	// --- Create ---
	// Params are the flat TaskPushNotificationConfig shape: config fields sit
	// beside taskId at the top level (A2A v1.0 proto wire format).
	createResp := rpcCall(t, ts.URL, methodCreatePushConfig, cfg)
	if createResp.Error != nil {
		t.Fatalf("Create: unexpected error: %+v", createResp.Error)
	}
	var created taskPushConfig
	if err := json.Unmarshal(createResp.Result, &created); err != nil {
		t.Fatalf("Create: decode result: %v", err)
	}
	if created.URL != cfg.URL {
		t.Errorf("Create: URL mismatch: got %q want %q", created.URL, cfg.URL)
	}
	if created.ID == "" {
		t.Error("Create: server must assign a non-empty ID")
	}
	if created.TaskID != taskID {
		t.Errorf("Create: taskId mismatch: got %q want %q", created.TaskID, taskID)
	}

	// --- Get ---
	getResp := rpcCall(t, ts.URL, methodGetPushConfig, taskPushConfig{
		TaskID:                 taskID,
		PushNotificationConfig: PushNotificationConfig{ID: created.ID},
	})
	if getResp.Error != nil {
		t.Fatalf("Get: unexpected error: %+v", getResp.Error)
	}
	var got taskPushConfig
	if err := json.Unmarshal(getResp.Result, &got); err != nil {
		t.Fatalf("Get: decode result: %v", err)
	}
	if got.ID != created.ID || got.URL != cfg.URL {
		t.Errorf("Get: got %+v, want id=%s url=%s", got, created.ID, cfg.URL)
	}
	if got.TaskID != taskID {
		t.Errorf("Get: taskId mismatch: got %q want %q", got.TaskID, taskID)
	}

	// --- List (1 element) ---
	listResp := rpcCall(t, ts.URL, methodListPushConfigs, taskPushConfig{TaskID: taskID})
	if listResp.Error != nil {
		t.Fatalf("List(1): unexpected error: %+v", listResp.Error)
	}
	var configs []taskPushConfig
	if err := json.Unmarshal(listResp.Result, &configs); err != nil {
		t.Fatalf("List(1): decode result: %v", err)
	}
	if len(configs) != 1 || configs[0].ID != created.ID {
		t.Errorf("List(1): got %+v, want 1 element with id=%s", configs, created.ID)
	}
	if configs[0].TaskID != taskID {
		t.Errorf("List(1): element taskId mismatch: got %q want %q", configs[0].TaskID, taskID)
	}

	// --- Delete ---
	delResp := rpcCall(t, ts.URL, methodDeletePushConfig, taskPushConfig{
		TaskID:                 taskID,
		PushNotificationConfig: PushNotificationConfig{ID: created.ID},
	})
	if delResp.Error != nil {
		t.Fatalf("Delete: unexpected error: %+v", delResp.Error)
	}

	// --- List (empty after delete) ---
	listResp2 := rpcCall(t, ts.URL, methodListPushConfigs, taskPushConfig{TaskID: taskID})
	if listResp2.Error != nil {
		t.Fatalf("List(0): unexpected error: %+v", listResp2.Error)
	}
	var configs2 []taskPushConfig
	if err := json.Unmarshal(listResp2.Result, &configs2); err != nil {
		t.Fatalf("List(0): decode result: %v", err)
	}
	if len(configs2) != 0 {
		t.Errorf("List(0): got %d elements, want 0", len(configs2))
	}

	// --- Not-found: all methods on missing task ---
	missing := "no-such-task-id"
	notFoundMethods := []struct {
		method string
		params taskPushConfig
	}{
		{methodCreatePushConfig, taskPushConfig{TaskID: missing, PushNotificationConfig: PushNotificationConfig{URL: "https://example.com/hook"}}},
		{methodGetPushConfig, taskPushConfig{TaskID: missing, PushNotificationConfig: PushNotificationConfig{ID: "x"}}},
		{methodListPushConfigs, taskPushConfig{TaskID: missing}},
		{methodDeletePushConfig, taskPushConfig{TaskID: missing, PushNotificationConfig: PushNotificationConfig{ID: "x"}}},
	}
	for _, tc := range notFoundMethods {
		resp := rpcCall(t, ts.URL, tc.method, tc.params)
		if resp.Error == nil || resp.Error.Code != codeTaskNotFound {
			t.Errorf("%s on missing task: want %d, got %+v", tc.method, codeTaskNotFound, resp.Error)
		}
	}

	// --- Push-disabled server: all methods return codePushNotSupported ---
	disabledSrv := NewServer(a2atest.NewEchoAgent("echo", "echoes")) // no WithPushNotifications
	disabledTs := httptest.NewServer(disabledSrv)
	defer disabledTs.Close()

	allMethods := []struct {
		method string
		params taskPushConfig
	}{
		{methodCreatePushConfig, taskPushConfig{TaskID: "x", PushNotificationConfig: PushNotificationConfig{URL: "https://example.com/hook"}}},
		{methodGetPushConfig, taskPushConfig{TaskID: "x", PushNotificationConfig: PushNotificationConfig{ID: "y"}}},
		{methodListPushConfigs, taskPushConfig{TaskID: "x"}},
		{methodDeletePushConfig, taskPushConfig{TaskID: "x", PushNotificationConfig: PushNotificationConfig{ID: "y"}}},
	}
	for _, tc := range allMethods {
		resp := rpcCall(t, disabledTs.URL, tc.method, tc.params)
		if resp.Error == nil || resp.Error.Code != codePushNotSupported {
			t.Errorf("%s on disabled server: want %d, got %+v", tc.method, codePushNotSupported, resp.Error)
		}
	}
}
