package a2a_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/oasis/a2a"
	"github.com/nevindra/oasis/a2a/a2atest"
	"github.com/nevindra/oasis/core"
)

// recordingStore is a minimal external (a2a_test package) implementation of
// a2a.TaskStore. Its sole purpose is to prove the interface is implementable
// from outside the a2a package — the regression guard for the defect where
// TaskStore's methods named the unexported *taskEntry, making the interface
// (and WithTaskStore) impossible to satisfy from another package.
//
// It honors the same-instance-while-live contract: a live task is the same
// *a2a.TaskRecord pointer the server Saved, returned unchanged from Get/List,
// so the server's in-place mutations remain observable to pollers.
type recordingStore struct {
	mu    sync.Mutex
	items map[string]*a2a.TaskRecord
	saves int
	gets  int
}

func newRecordingStore() *recordingStore {
	return &recordingStore{items: make(map[string]*a2a.TaskRecord)}
}

func (s *recordingStore) Save(_ context.Context, rec *a2a.TaskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	// Store the exact instance: live tasks are mutated in place by the server.
	s.items[rec.Task.ID] = rec
	return nil
}

func (s *recordingStore) Get(_ context.Context, id string) (*a2a.TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	rec, ok := s.items[id]
	if !ok {
		return nil, a2a.ErrTaskNotFound
	}
	return rec, nil
}

func (s *recordingStore) List(_ context.Context, contextID string) ([]*a2a.TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*a2a.TaskRecord{}
	for _, rec := range s.items {
		if rec.Task.ContextID == contextID {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (s *recordingStore) counts() (saves, gets int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves, s.gets
}

// ids returns every task ID the store currently holds.
func (s *recordingStore) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.items))
	for id := range s.items {
		out = append(out, id)
	}
	return out
}

// TestE2ECustomTaskStore proves a TaskStore implemented in an external package
// can be plugged via a2a.WithTaskStore and that the server drives it: one
// SendMessage round-trip (via a2a.Dial + Execute) must record Save calls and
// the stored record must be retrievable through the store's own Get. This is
// the compile-time-and-runtime regression test for the unimplementable-interface
// defect: if TaskStore's signatures named an unexported type, recordingStore
// would not compile against a2a.TaskStore and WithTaskStore could not accept it.
func TestE2ECustomTaskStore(t *testing.T) {
	store := newRecordingStore()
	// Compile-time proof: an external type satisfies a2a.TaskStore and is
	// accepted by WithTaskStore.
	var _ a2a.TaskStore = store

	ts := a2atest.Serve(t, a2a.NewServer(
		a2atest.NewEchoAgent("echo", "echoes"),
		a2a.WithTaskStore(store),
	))
	remote, err := a2a.Dial(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	res, err := remote.Execute(context.Background(), core.AgentTask{Input: "hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "echo: hello" {
		t.Fatalf("Output = %q, want %q", res.Output, "echo: hello")
	}

	if saves, _ := store.counts(); saves == 0 {
		t.Fatal("custom store saw no Save calls; server did not persist through it")
	}

	// The single round-trip created exactly one task. Read it back through the
	// store's own Get to confirm it serves what it saved, and that the in-place
	// mutations the server made are visible (state reached completed).
	ids := store.ids()
	if len(ids) != 1 {
		t.Fatalf("store holds %d tasks, want 1", len(ids))
	}
	taskID := ids[0]
	got, err := store.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.Task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("stored task state = %q, want %q", got.Task.Status.State, a2a.TaskStateCompleted)
	}

	// A GetTask over the wire routes the server through store.Get — proving the
	// server reads through the custom store, not just writes to it. (The blocking
	// SendMessage above settled inline and returned without polling, so this is
	// the read path's regression guard.)
	if _, err := remote.Client().GetTask(context.Background(), taskID); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if _, gets := store.counts(); gets == 0 {
		t.Fatal("custom store saw no Get calls after GetTask; server did not read through it")
	}

	// Listing by the stored task's context must return the same record instance.
	recs, err := store.List(context.Background(), got.Task.ContextID)
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(recs) != 1 || recs[0].Task.ID != taskID {
		t.Fatalf("store.List returned %d records, want the one saved task", len(recs))
	}
}

// TestE2EConcurrentClients verifies that 32 concurrent goroutines hammering one
// server through one RemoteAgent each receive a response that matches exactly
// what they sent — confirming there is no cross-task output bleed under
// concurrent load.
func TestE2EConcurrentClients(t *testing.T) {
	ts := a2atest.Serve(t, a2a.NewServer(a2atest.NewEchoAgent("echo", "echoes")))
	remote, err := a2a.Dial(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	// Why: buffered to 32 so goroutines never block on send, avoiding
	// a deadlock if wg.Wait returns before all goroutines finish writing.
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			input := fmt.Sprintf("msg-%d", n)
			res, err := remote.Execute(context.Background(), core.AgentTask{Input: input})
			if err != nil {
				errs <- err
				return
			}
			if res.Output != "echo: "+input {
				errs <- fmt.Errorf("cross-task bleed: sent %q got %q", input, res.Output)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestE2ECancelPropagation verifies that calling Execute with an already-canceled
// context returns an error immediately rather than hanging or succeeding.
func TestE2ECancelPropagation(t *testing.T) {
	ts := a2atest.Serve(t, a2a.NewServer(a2atest.NewEchoAgent("echo", "echoes")))
	remote, err := a2a.Dial(context.Background(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled
	if _, err := remote.Execute(ctx, core.AgentTask{Input: "x"}); err == nil {
		t.Error("Execute with canceled context must error")
	}
}

// TestE2ESuspendResumeAcrossWire is the flagship multi-turn HITL test: it
// drives a SuspendingAgent through a full suspend→resume cycle over the wire
// using only the public a2a and core APIs, with no server-internal knowledge.
//
// First Execute on a fresh ThreadID must suspend (FinishSuspended + non-empty
// SuspendPayload). Second Execute on the same ThreadID must resume the pending
// remote task and complete (FinishStop + non-empty Output).
func TestE2ESuspendResumeAcrossWire(t *testing.T) {
	ts := a2atest.Serve(t, a2a.NewServer(a2atest.NewSuspendingAgent("hitl", "asks first")))
	remote, err := a2a.Dial(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	// First turn: expect the task to suspend and provide a payload (the question
	// the agent needs answered before it can continue).
	first, err := remote.Execute(context.Background(), core.AgentTask{
		Input:    "start",
		ThreadID: "th-e2e",
	})
	if err != nil {
		t.Fatalf("Execute(first): %v", err)
	}
	if first.FinishReason != core.FinishSuspended {
		t.Fatalf("first FinishReason = %q, want %q", first.FinishReason, core.FinishSuspended)
	}
	if len(first.SuspendPayload) == 0 {
		t.Error("first SuspendPayload is empty; want the agent's question")
	}

	// Second turn: same ThreadID so RemoteAgent carries the pending task ID,
	// resuming the suspended task. Expect a complete result.
	second, err := remote.Execute(context.Background(), core.AgentTask{
		Input:    "the answer",
		ThreadID: "th-e2e",
	})
	if err != nil {
		t.Fatalf("Execute(resume): %v", err)
	}
	if second.FinishReason != core.FinishStop {
		t.Errorf("resume FinishReason = %q, want %q", second.FinishReason, core.FinishStop)
	}
	if second.Output == "" {
		t.Error("resume Output is empty; want the completed result")
	}
}

// TestE2ENonBlockingPush exercises the exported SendConfiguration type through
// the public API end-to-end: a non-blocking send with a PushNotificationConfig
// returns a working task immediately, and the server POSTs a completed
// StreamResponse to the registered webhook once the background run settles.
//
// This test proves:
//   - SendConfiguration is accessible from external (a2a_test) callers.
//   - The non-blocking push flow completes without polling.
//   - The webhook receives a StreamResponse whose Task is completed.
func TestE2ENonBlockingPush(t *testing.T) {
	// --- Webhook receiver: captures the first POST body ---
	var received atomic.Pointer[a2a.StreamResponse]
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sr a2a.StreamResponse
		if err := json.NewDecoder(r.Body).Decode(&sr); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		received.Store(&sr)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)

	// --- A2A server with push notifications enabled ---
	ts := a2atest.Serve(t, a2a.NewServer(
		a2atest.NewEchoAgent("echo", "echoes"),
		a2a.WithPushNotifications(),
	))

	remote, err := a2a.Dial(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	// Construct a Message using only public a2a API.
	msg := a2a.Message{
		MessageID: "e2e-push-msg-1",
		Role:      a2a.RoleUser,
		Parts:     []a2a.Part{a2a.TextPart("hello push")},
	}

	// SendMessage with non-blocking config and push webhook.
	task, err := remote.Client().SendMessage(context.Background(), msg, &a2a.SendConfiguration{
		Blocking: a2a.NonBlockingPtr(),
		PushNotificationConfig: &a2a.PushNotificationConfig{
			URL:   hook.URL,
			Token: "e2e-token",
		},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Non-blocking send returns a working task immediately.
	if task.Status.State != a2a.TaskStateWorking {
		t.Errorf("immediate response state = %q, want %q", task.Status.State, a2a.TaskStateWorking)
	}

	// Poll for the webhook delivery (deadline 2 s).
	deadline := time.Now().Add(2 * time.Second)
	for received.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("webhook never received a POST within 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	sr := received.Load()
	if sr.Task == nil {
		t.Fatal("webhook payload has no Task")
	}
	if sr.Task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("webhook task state = %q, want %q", sr.Task.Status.State, a2a.TaskStateCompleted)
	}
}
