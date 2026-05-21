package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// The StreamEvent struct, StreamEventType, and Event* constants live in
// github.com/nevindra/oasis/core and are re-exported as aliases in
// types_aliases.go. The helpers below (ServeSSE, WriteSSEEvent) stay at root
// because they depend on the StreamingAgent / AgentResult / AgentTask
// abstractions that haven't moved out of the root package yet.

// ServeSSE streams an agent's response as Server-Sent Events over HTTP.
//
// It validates that w implements [http.Flusher], sets SSE headers, creates a
// buffered [StreamEvent] channel, runs the agent in a background goroutine,
// and writes each event as:
//
//	event: <event-type>
//	data: <json-encoded StreamEvent>
//
// The stream emits [EventRunStart] as the first event and [EventRunFinish] as
// the last event inside the channel loop. [EventRunFinish] carries the
// [FinishReason] and any provider warnings or metadata. After the channel
// closes, a final "done" SSE event is written for legacy clients that wait on
// it. New clients should read [EventRunFinish] for structured completion data.
//
// On completion it sends a final "done" event. If the agent returns an error,
// it is sent as an "error" event before returning.
//
// Client disconnection propagates via ctx cancellation to the agent.
// Callers typically pass r.Context() as ctx.
func ServeSSE(ctx context.Context, w http.ResponseWriter, agent StreamingAgent, task AgentTask) (AgentResult, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return AgentResult{}, fmt.Errorf("ResponseWriter does not implement http.Flusher")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan StreamEvent, 64)
	safeClose := onceClose(ch)

	type execResult struct {
		result AgentResult
		err    error
	}
	resultCh := make(chan execResult, 1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				// Ensure ch is closed so the for-range loop below
				// doesn't block forever, then signal the error.
				// Use sync.Once because ExecuteStream may have already
				// closed ch before the panic site.
				safeClose()
				resultCh <- execResult{AgentResult{}, fmt.Errorf("agent panic: %v", p)}
				return
			}
		}()
		r, err := agent.ExecuteStream(ctx, task, ch)
		resultCh <- execResult{r, err}
	}()

	for ev := range ch {
		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
		flusher.Flush()
	}

	res := <-resultCh

	if res.err != nil {
		errData, _ := json.Marshal(map[string]string{"error": res.err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", errData)
		flusher.Flush()
		return res.result, res.err
	}

	doneData, _ := json.Marshal(res.result)
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", doneData)
	flusher.Flush()

	return res.result, nil
}

// WriteSSEEvent writes a single Server-Sent Event to w and flushes.
// It validates that w implements [http.Flusher], JSON-marshals data into
// the SSE data field, and flushes immediately. eventType is the SSE event
// name (e.g. "text-delta", "done").
//
// Use this to compose custom SSE loops with [StreamingAgent.ExecuteStream]:
//
//	ch := make(chan oasis.StreamEvent, 64)
//	go agent.ExecuteStream(ctx, task, ch)
//	for ev := range ch {
//	    oasis.WriteSSEEvent(w, string(ev.Type), ev)
//	}
//	oasis.WriteSSEEvent(w, "done", customPayload)
func WriteSSEEvent(w http.ResponseWriter, eventType string, data any) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("ResponseWriter does not implement http.Flusher")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal sse data: %w", err)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, encoded)
	flusher.Flush()
	return nil
}
