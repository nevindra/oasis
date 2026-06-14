package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nevindra/oasis/core"
)

// agentTaskFromMessage maps an inbound A2A Message onto a core.AgentTask.
//
//   - text parts        → Input, newline-joined in part order.
//   - data parts        → a ```json fenced block appended to Input. The bytes
//     pass through verbatim (never re-decoded/re-encoded) so structured
//     payloads survive the round-trip byte-for-byte.
//   - raw / url parts    → Attachments (inline bytes or a URL reference).
//   - contextId          → ThreadID (the conversation scope).
//
// Empty parts are tolerated here; the handler rejects a message with no parts
// before calling this.
func agentTaskFromMessage(msg Message) core.AgentTask {
	var b strings.Builder
	var attachments []core.Attachment
	for _, p := range msg.Parts {
		switch {
		case p.Text != "":
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		case len(p.Data) > 0:
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("```json\n")
			b.Write(p.Data) // raw byte passthrough — no re-encode
			b.WriteString("\n```")
		case len(p.Raw) > 0:
			attachments = append(attachments, core.Attachment{MimeType: p.MediaType, Data: p.Raw})
		case p.URL != "":
			attachments = append(attachments, core.Attachment{MimeType: p.MediaType, URL: p.URL})
		}
	}
	return core.AgentTask{
		Input:       b.String(),
		Attachments: attachments,
		ThreadID:    msg.ContextID,
	}
}

// artifactsFromResult turns a completed run into a single "response" artifact.
// Output becomes a text part, Object a data part, and Files/Attachments file
// parts. Bytes are referenced, never copied (zero-copy passthrough). Returns
// nil when the run produced no content at all.
func artifactsFromResult(res core.AgentResult) []Artifact {
	var parts []Part
	if res.Output != "" {
		parts = append(parts, Part{Text: res.Output})
	}
	if len(res.Object) > 0 {
		parts = append(parts, Part{Data: res.Object}) // reference, no copy
	}
	for _, f := range res.Files {
		parts = append(parts, filePart(f))
	}
	for _, a := range res.Attachments {
		parts = append(parts, filePart(a))
	}
	if len(parts) == 0 {
		return nil
	}
	return []Artifact{{ArtifactID: newID(), Name: "response", Parts: parts}}
}

// filePart maps a core.Attachment onto an A2A file part: inline bytes go in
// Raw (referenced, not copied), a remote reference in URL.
func filePart(a core.Attachment) Part {
	p := Part{MediaType: a.MimeType}
	if len(a.Data) > 0 {
		p.Raw = a.Data // reference, no copy
	} else {
		p.URL = a.URL
	}
	return p
}

// agentMessage builds an agent-role status message carrying text (e.g. the
// suspend question or a failure explanation).
func agentMessage(taskID, contextID, text string) *Message {
	return &Message{
		MessageID: newID(),
		TaskID:    taskID,
		ContextID: contextID,
		Role:      RoleAgent,
		Parts:     []Part{{Text: text}},
	}
}

// handleMessageSend implements the SendMessage method. A new message (no
// TaskID) starts a fresh task; a message carrying a TaskID resumes a suspended
// run (resumeTask). The result is always the sendResult oneof wrapping the
// settled (or working) task — never a bare Task.
func (s *Server) handleMessageSend(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p sendParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if len(p.Message.Parts) == 0 {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params: message has no parts"}
	}

	// A TaskID on the inbound message means "continue this task" — resume.
	if p.Message.TaskID != "" {
		return s.resumeTask(ctx, p)
	}

	at := agentTaskFromMessage(p.Message)
	contextID := p.Message.ContextID
	if contextID == "" {
		contextID = newID() // the server assigns a conversation scope
		at.ThreadID = contextID
	}

	taskID := newID()
	entry := &TaskRecord{
		Task: Task{
			ID:        taskID,
			ContextID: contextID,
			Status:    TaskStatus{State: TaskStateSubmitted, Timestamp: nowRFC3339()},
			History:   []Message{p.Message},
		},
	}

	// Non-blocking path: only valid when the client explicitly opted out of
	// blocking AND registered a push config to receive the eventual result.
	// Blocking is the default (nil/true Blocking), so a zero-value or absent
	// Configuration runs the inline blocking path below.
	if p.Configuration.isNonBlocking() && p.Configuration.PushNotificationConfig != nil {
		if !s.opts.pushEnabled {
			return nil, &rpcError{Code: codePushNotSupported, Message: ErrPushNotSupported.Error()}
		}
		entry.push = p.Configuration.PushNotificationConfig
		entry.Task.Status = TaskStatus{State: TaskStateWorking, Timestamp: nowRFC3339()}
		if err := s.store.Save(ctx, entry); err != nil {
			return nil, &rpcError{Code: codeInternalError, Message: "save task: " + err.Error()}
		}
		// The HTTP request ends here, but the run must continue — use the
		// server-lifetime context so Close() can cancel background runs.
		bg := s.baseCtx
		go func() {
			defer func() {
				if v := recover(); v != nil {
					s.settle(bg, entry, core.AgentResult{}, fmt.Errorf("agent panicked: %v", v))
				}
			}()
			s.runTask(bg, entry, at)
		}()
		snapshot := s.snapshot(entry)
		return sendResult{Task: &snapshot}, nil
	}

	// Blocking path (the default): run inline, return the settled task.
	if err := s.store.Save(ctx, entry); err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "save task: " + err.Error()}
	}
	s.runTask(ctx, entry, at)
	snapshot := s.snapshot(entry)
	return sendResult{Task: &snapshot}, nil
}

// runTask executes the agent and settles the entry in place. It NEVER surfaces
// an agent error as an RPC error — a failed run is a failed TASK with an
// actionable status message. Execute is called outside entry.mu (lock
// discipline); the result is applied under the lock.
func (s *Server) runTask(ctx context.Context, entry *TaskRecord, at core.AgentTask) {
	runCtx, cancel := context.WithCancel(ctx)

	// Defensive recover: covers the inline (blocking) path. The background path
	// has its own recover wrapper in the launching goroutine; this one handles
	// the blocking path where net/http would 500 the connection — a settled
	// FAILED task is strictly better. Note: cancel is NOT called before settle
	// here — the panic fires before the post-Execute cancel() at line below, so
	// runCtx is still live (Err() == nil), ensuring settle sees FAILED not CANCELED.
	defer func() {
		if v := recover(); v != nil {
			s.settle(runCtx, entry, core.AgentResult{}, fmt.Errorf("agent panicked: %v", v))
			cancel()
		}
	}()

	// Publish the cancel handle and move to working before the call, so a
	// concurrent CancelTask (Task 7) can abort the run.
	entry.mu.Lock()
	entry.cancel = cancel
	entry.Task.Status = TaskStatus{State: TaskStateWorking, Timestamp: nowRFC3339()}
	entry.mu.Unlock()

	res, err := s.agent.Execute(runCtx, at) // no lock held
	// Settle before cancel so that runCtx.Err() reflects external cancellation
	// (user-initiated via entry.cancel) rather than the local cleanup cancel.
	s.settle(runCtx, entry, res, err)
	cancel()
}

// settle applies a finished run to the entry, persists it, and (if a push
// config is registered) delivers the terminal snapshot. The locking window
// only mutates in-memory state; Save and deliverPush run outside the lock.
//
// runCtx is the context that was passed to agent.Execute (the one entry.cancel
// aborts). Using runCtx for the CANCELED decision is correct: when Close fires,
// s.baseCtx is canceled, which cancels runCtx too, so background runs settle
// CANCELED as expected. The parent ctx is used only for store operations.
func (s *Server) settle(runCtx context.Context, entry *TaskRecord, res core.AgentResult, err error) {
	entry.mu.Lock()
	taskID := entry.Task.ID
	contextID := entry.Task.ContextID
	entry.cancel = nil

	switch {
	case err != nil:
		// A suspend reports as an error whose value satisfies resumable.
		var r resumable
		if res.FinishReason == core.FinishSuspended && errorsAs(err, &r) {
			entry.resume = r
			entry.Task.Status = TaskStatus{
				State:     TaskStateInputRequired,
				Message:   agentMessage(taskID, contextID, suspendQuestion(res)),
				Timestamp: nowRFC3339(),
			}
			break
		}
		// Determine CANCELED vs FAILED from the run's own context and the error
		// returned by Execute. This is correct for both the blocking path (runCtx
		// derives from the HTTP request context) and the background path (runCtx
		// derives from s.baseCtx, whose Err() is set when Close fires).
		state := TaskStateFailed
		if errors.Is(err, context.Canceled) || runCtx.Err() != nil {
			state = TaskStateCanceled
		}
		entry.Task.Status = TaskStatus{
			State:     state,
			Message:   agentMessage(taskID, contextID, "agent execution failed: "+err.Error()),
			Timestamp: nowRFC3339(),
		}
	default:
		entry.Task.Artifacts = artifactsFromResult(res)
		entry.Task.Status = TaskStatus{State: TaskStateCompleted, Timestamp: nowRFC3339()}
	}

	// snapshot shares History/Artifacts backing arrays with the live entry;
	// safe today because settled entries are no longer mutated — revisit if
	// streaming mutates artifacts in place.
	snapshot := entry.Task // value copy under lock
	push := entry.push
	entry.mu.Unlock()

	if err := s.store.Save(runCtx, entry); err != nil {
		slog.Error("a2a: persist settled task failed", "task", taskID, "state", snapshot.Status.State, "err", err)
	}
	if push != nil {
		// Why: pass s.baseCtx, not runCtx — runCtx may already be canceled
		// (e.g. CancelTask flow), yet delivery of the final state is still
		// required. Only Server.Close() (which cancels s.baseCtx) should
		// suppress webhook delivery.
		s.deliverPush(s.baseCtx, push, snapshot)
	}
}

// resumeTask continues a suspended (input-required) task with a follow-up
// message. The follow-up text is JSON-encoded and handed to the agent's
// single-use resumable. Re-suspension (multi-round HITL) re-arms the resumable
// and returns input-required again.
//
// Concurrent resumes of the same task are safe (the resumable is claimed under
// the lock as a single-use operation), but History/snapshot interleaving is
// unspecified; clients should serialize resumes per task.
func (s *Server) resumeTask(ctx context.Context, p sendParams) (any, *rpcError) {
	entry, err := s.store.Get(ctx, p.Message.TaskID)
	if err != nil {
		return nil, &rpcError{Code: codeTaskNotFound, Message: err.Error()}
	}

	// Snapshot + claim the resumable under the lock; operate outside it.
	entry.mu.Lock()
	state := entry.Task.Status.State
	r := entry.resume
	if state != TaskStateInputRequired || r == nil {
		entry.mu.Unlock()
		return nil, &rpcError{
			Code:    codeUnsupportedOp,
			Message: "task is not awaiting input (state " + string(state) + ")",
		}
	}
	entry.resume = nil // single-use: a resumable may only be consumed once
	entry.Task.History = append(entry.Task.History, p.Message)
	entry.Task.Status = TaskStatus{State: TaskStateWorking, Timestamp: nowRFC3339()}
	at := agentTaskFromMessage(p.Message)
	entry.mu.Unlock()

	data, _ := json.Marshal(at.Input) // cannot fail: marshaling a plain string
	res, rerr := r.Resume(ctx, data)  // no lock held

	s.settle(ctx, entry, res, rerr)
	snapshot := s.snapshot(entry)
	return sendResult{Task: &snapshot}, nil
}

// snapshot returns a value copy of the entry's task taken under the entry lock.
func (s *Server) snapshot(entry *TaskRecord) Task {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.Task
}

// suspendQuestion extracts the human-facing question text from a suspended
// run. The agent's question travels as the SuspendPayload; fall back to the
// Output, then a generic prompt, so the input-required status always carries
// something actionable.
func suspendQuestion(res core.AgentResult) string {
	if len(res.SuspendPayload) > 0 {
		return string(res.SuspendPayload)
	}
	if res.Output != "" {
		return res.Output
	}
	return "additional input required"
}

// handleUnsupportedVersion answers methods this version deliberately omits
// (ListTasks, GetExtendedAgentCard) — a documented YAGNI, not a stub.
func (s *Server) handleUnsupportedVersion(context.Context, json.RawMessage) (any, *rpcError) {
	return nil, &rpcError{Code: codeUnsupportedOp, Message: "not implemented in this version"}
}
