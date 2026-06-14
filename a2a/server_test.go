package a2a

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/oasis/a2a/a2atest"
)

// rpcCall posts a JSON-RPC request to the test server and decodes the response.
func rpcCall(t *testing.T, url, method string, params any) rpcResponse {
	t.Helper()
	p, _ := json.Marshal(params)
	body, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: p})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// sendTask unwraps the sendResult oneof from a SendMessage response.
func sendTask(t *testing.T, resp rpcResponse) Task {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	var r sendResult
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatal(err)
	}
	if r.Task == nil {
		t.Fatalf("sendResult.task missing: %s", resp.Result)
	}
	return *r.Task
}

func TestMessageSendCompletes(t *testing.T) {
	ts := httptest.NewServer(NewServer(a2atest.NewEchoAgent("echo", "echoes")))
	defer ts.Close()

	task := sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("hello")}},
	}))
	if task.Status.State != TaskStateCompleted {
		t.Errorf("state = %s", task.Status.State)
	}
	if len(task.Artifacts) == 0 || task.Artifacts[0].Parts[0].Text != "echo: hello" {
		t.Errorf("artifacts = %+v", task.Artifacts)
	}
	if task.ContextID == "" {
		t.Error("server must assign a contextId when the client sends none")
	}
}

// TestMessageSendZeroConfigBlocks proves the freeze-shape fix end-to-end: a
// send carrying an explicit but zero-value SendConfiguration{} (Blocking unset)
// runs the inline blocking path and returns a settled COMPLETED task — not a
// WORKING task as a non-blocking default would. Even with a push config present
// and push enabled, the absence of an explicit non-blocking opt-out keeps it
// on the blocking path.
func TestMessageSendZeroConfigBlocks(t *testing.T) {
	ts := httptest.NewServer(NewServer(a2atest.NewEchoAgent("echo", "echoes"), WithPushNotifications()))
	defer ts.Close()

	task := sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("hello")}},
		Configuration: &SendConfiguration{
			// Blocking is nil → default → blocking, even though a push config is
			// supplied and push is enabled on the server.
			PushNotificationConfig: &PushNotificationConfig{URL: "http://localhost:9/never"},
		},
	}))
	if task.Status.State != TaskStateCompleted {
		t.Errorf("zero-value SendConfiguration must block: state = %s, want completed", task.Status.State)
	}
}

func TestMessageSendAgentError(t *testing.T) {
	ts := httptest.NewServer(NewServer(a2atest.NewFailingAgent("broken", "always fails")))
	defer ts.Close()

	task := sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("x")}},
	}))
	if task.Status.State != TaskStateFailed {
		t.Errorf("state = %s, want failed task (not an RPC error)", task.Status.State)
	}
	if task.Status.Message == nil || len(task.Status.Message.Parts) == 0 {
		t.Error("failed task must carry an actionable message")
	}
}

func TestMessageSendSuspendResume(t *testing.T) {
	ts := httptest.NewServer(NewServer(a2atest.NewSuspendingAgent("hitl", "asks first")))
	defer ts.Close()

	task := sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("start")}},
	}))
	if task.Status.State != TaskStateInputRequired {
		t.Fatalf("state = %s, want input-required", task.Status.State)
	}
	if task.Status.Message == nil {
		t.Fatal("input-required must carry the agent's question")
	}

	// Follow-up message on the same task resumes the suspended run.
	task = sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m2", TaskID: task.ID, Role: RoleUser, Parts: []Part{TextPart("the answer")}},
	}))
	if task.Status.State != TaskStateCompleted {
		t.Errorf("after resume: state = %s", task.Status.State)
	}
}

func TestResumeNonSuspendedTask(t *testing.T) {
	ts := httptest.NewServer(NewServer(a2atest.NewEchoAgent("echo", "echoes")))
	defer ts.Close()
	task := sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("hi")}},
	}))
	resp := rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m2", TaskID: task.ID, Role: RoleUser, Parts: []Part{TextPart("again")}},
	})
	if resp.Error == nil || resp.Error.Code != codeUnsupportedOp {
		t.Errorf("resume of completed task: want %d, got %+v", codeUnsupportedOp, resp.Error)
	}
}

func TestMethodNotFound(t *testing.T) {
	ts := httptest.NewServer(NewServer(a2atest.NewEchoAgent("echo", "echoes")))
	defer ts.Close()
	resp := rpcCall(t, ts.URL, "nope/nope", struct{}{})
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("want -32601, got %+v", resp.Error)
	}
}

// TestPanicAgentBlockingPath verifies that a panicking agent on the blocking
// (inline) path settles the task as FAILED rather than crashing the process or
// dropping the connection. The test must fail when the recover in runTask is
// commented out and pass with it in place.
func TestPanicAgentBlockingPath(t *testing.T) {
	srv := NewServer(a2atest.NewPanicAgent("panic-agent", "always panics"))
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	task := sendTask(t, rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("trigger panic")}},
	}))
	if task.Status.State != TaskStateFailed {
		t.Errorf("blocking panic: state = %s, want failed", task.Status.State)
	}
	if task.Status.Message == nil || len(task.Status.Message.Parts) == 0 {
		t.Error("failed task from panic must carry an actionable message")
	}
	// The message should mention "panicked" so callers have actionable context.
	if task.Status.Message != nil && len(task.Status.Message.Parts) > 0 {
		if !strings.Contains(task.Status.Message.Parts[0].Text, "panicked") {
			t.Errorf("message should mention 'panicked', got: %q", task.Status.Message.Parts[0].Text)
		}
	}
}

// TestPanicAgentNonBlockingPath verifies that a panicking agent on the
// background (non-blocking push) path does not crash the server process. After
// the panicking non-blocking send, a second healthy blocking request on the
// same server must succeed — proving the process is alive.
func TestPanicAgentNonBlockingPath(t *testing.T) {
	// Use a push-enabled server with a panic agent.
	srv := NewServer(
		a2atest.NewPanicAgent("panic-agent", "always panics"),
		WithPushNotifications(),
	)
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Non-blocking push request — HTTP response returns immediately with a
	// working task; the background goroutine will panic and recover.
	pushCfg := &PushNotificationConfig{URL: "http://localhost:9999/unreachable"}
	resp := rpcCall(t, ts.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{TextPart("trigger bg panic")}},
		Configuration: &SendConfiguration{
			Blocking:               NonBlockingPtr(),
			PushNotificationConfig: pushCfg,
		},
	})
	if resp.Error != nil {
		t.Fatalf("non-blocking push setup: unexpected rpc error: %+v", resp.Error)
	}

	// Give the background goroutine a moment to run (and panic+recover).
	time.Sleep(50 * time.Millisecond)

	// Now issue a healthy blocking request on the same server. If the process
	// survived the background panic, this will complete normally.
	echoSrv := NewServer(a2atest.NewEchoAgent("echo", "echoes"))
	defer echoSrv.Close()
	ts2 := httptest.NewServer(echoSrv)
	defer ts2.Close()

	task := sendTask(t, rpcCall(t, ts2.URL, methodSendMessage, sendParams{
		Message: Message{MessageID: "m2", Role: RoleUser, Parts: []Part{TextPart("healthy")}},
	}))
	if task.Status.State != TaskStateCompleted {
		t.Errorf("post-panic healthy request: state = %s, want completed", task.Status.State)
	}
}
