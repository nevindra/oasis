package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/workflow"
)

// Re-export workflow types used in suspend/resume machinery.
type StepStatus = workflow.StepStatus
type WorkflowContext = workflow.WorkflowContext

// --- Suspend / Resume ---

// defaultSuspendTTL is the default time-to-live for ErrSuspended snapshots.
// When the TTL elapses without Resume(), the resume closure and captured
// message snapshot are released automatically, preventing memory leaks.
// Callers can override with WithSuspendTTL after receiving ErrSuspended.
const defaultSuspendTTL = 30 * time.Minute

const defaultMaxSuspendSnapshots = 20
const defaultMaxSuspendBytes int64 = 256 * 1024 * 1024 // 256 MB

// defaultResumeFormat is the formatter used by untyped Suspend (and as a
// fallback inside protocol suspends when their formatter is nil for any
// reason). It preserves the exact byte-for-byte output Oasis has emitted
// since v0.1 — callers reading transcripts can rely on it.
func defaultResumeFormat(data json.RawMessage) string {
	return "Human input: " + string(data)
}

// errSuspend is the internal sentinel returned by step functions to signal
// that execution should pause for external input. The workflow/network engine
// catches it and converts to ErrSuspended with resume capabilities.
type errSuspend struct {
	payload json.RawMessage
	tag     string // empty for untyped Suspend; protocol name for typed SuspendProtocol.Suspend
	// format produces the user-visible message injected into the LLM history
	// from the resume bytes. When nil, the default formatter (defaultResumeFormat)
	// is used, preserving today's "Human input: <data>" output.
	format func(data json.RawMessage) string
}

func (e *errSuspend) Error() string { return "suspend" }

// ErrSuspended is returned by Execute() when a workflow step or network
// processor suspends execution to await external input.
// Inspect Payload for context, then call Resume() or ResumeStream() with
// the human's response.
//
// Retention: ErrSuspended holds closures that capture the full conversation
// message history (including tool call arguments, results, and attachments).
// This data remains in memory until Resume()/ResumeStream() is called,
// Release() is called, the TTL expires, or the ErrSuspended value is
// garbage-collected.
//
// To prevent memory leaks in server environments, use WithSuspendTTL to set
// an automatic expiry. When the TTL elapses without Resume(), the snapshot
// is released automatically. Without a TTL, callers must call Release()
// explicitly when the resume window has passed (e.g. timeout, user abandonment).
// After release (manual or automatic), Resume()/ResumeStream() returns an error.
type ErrSuspended struct {
	// Step is the name of the step or processor hook that suspended.
	Step string
	// Payload carries context for the human (what to show, what to decide).
	Payload json.RawMessage
	// tag identifies the SuspendProtocol used to construct this suspension,
	// or "" if constructed via the untyped Suspend(json.RawMessage) path.
	// Protocol methods (PayloadFrom, Resume, ResumeStream) check tag for
	// mismatch and return a clear error.
	tag string
	// resume is the closure that continues execution with human input.
	// Guarded by mu when a TTL timer is active (timer callback writes from
	// a separate goroutine). Without a TTL, single-goroutine access is safe.
	resume func(ctx context.Context, data json.RawMessage) (AgentResult, error)
	// resumeStream is like resume but emits StreamEvent values into ch.
	// Set by workflow and agent-level suspend for streaming resume support.
	resumeStream func(ctx context.Context, data json.RawMessage, ch chan<- core.StreamEvent) (AgentResult, error)
	// mu guards resume/resumeStream against concurrent access from the TTL timer goroutine.
	mu sync.Mutex
	// ttlTimer is the auto-release timer. Nil when no TTL is set.
	ttlTimer *time.Timer
	// snapshotSize is the estimated bytes of the captured snapshot.
	snapshotSize int64
	// onRelease decrements the agent's suspend budget counters.
	onRelease func(size int64)
}

func (e *ErrSuspended) Error() string {
	return fmt.Sprintf("suspended at step %q", e.Step)
}

// Resume continues execution with the human's response data.
// The data is made available to the step via ResumeData().
// Resume is single-use: calling it more than once is undefined behavior.
// Returns an error if called on a released, expired, or externally constructed ErrSuspended.
func (e *ErrSuspended) Resume(ctx context.Context, data json.RawMessage) (AgentResult, error) {
	e.mu.Lock()
	if e.ttlTimer != nil {
		e.ttlTimer.Stop()
	}
	fn := e.resume
	onRel := e.onRelease
	e.resume = nil // single-use: free the captured snapshot after resume
	e.resumeStream = nil
	e.onRelease = nil
	e.mu.Unlock()

	if fn == nil {
		return AgentResult{}, fmt.Errorf("ErrSuspended: resume closure is nil (released, expired, or constructed outside engine)")
	}
	if onRel != nil {
		onRel(e.snapshotSize)
	}
	return fn(ctx, data)
}

// ResumeStream continues execution with the human's response data, emitting
// StreamEvent values into ch throughout. Like Resume but with streaming support.
// The channel is closed when streaming completes.
// Returns an error if called on a released, expired, or externally constructed ErrSuspended,
// or if the suspend was created in a non-streaming context (resumeStream is nil).
func (e *ErrSuspended) ResumeStream(ctx context.Context, data json.RawMessage, ch chan<- core.StreamEvent) (AgentResult, error) {
	e.mu.Lock()
	if e.ttlTimer != nil {
		e.ttlTimer.Stop()
	}
	fn := e.resumeStream
	onRel := e.onRelease
	e.resume = nil
	e.resumeStream = nil
	e.onRelease = nil
	e.mu.Unlock()

	if fn == nil {
		close(ch)
		return AgentResult{}, fmt.Errorf("ErrSuspended: resumeStream closure is nil (released, expired, or not supported)")
	}
	if onRel != nil {
		onRel(e.snapshotSize)
	}
	return fn(ctx, data, ch)
}

// Release nils out the resume closure, eagerly freeing the captured message
// snapshot and all referenced data (tool arguments, attachments, etc.).
// Call this when the suspend will not be resumed (timeout, user abandonment).
// After Release(), Resume() returns an error. Safe to call multiple times.
func (e *ErrSuspended) Release() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ttlTimer != nil {
		e.ttlTimer.Stop()
	}
	if e.resume != nil && e.onRelease != nil {
		e.onRelease(e.snapshotSize)
		e.onRelease = nil // prevent double-decrement
	}
	e.resume = nil
	e.resumeStream = nil
}

// WithSuspendTTL sets an automatic expiry on the suspended state.
// When the TTL elapses without Resume() being called, the resume closure
// is released automatically, freeing the captured message snapshot.
//
// A default TTL of 30 minutes is applied automatically when ErrSuspended
// is created by the framework. Call this to override with a custom duration.
//
// d must be positive. A zero or negative duration fires the release timer
// immediately, which is almost never what callers want. To disable the
// default TTL entirely, omit Resume()/Release() management from your flow
// and rely on garbage collection — but be aware abandoned snapshots will
// hold their captured message slice until GC observes the ErrSuspended.
//
//	var suspended *oasis.ErrSuspended
//	if errors.As(err, &suspended) {
//	    suspended.WithSuspendTTL(5 * time.Minute)
//	    // ... store suspended for later resume ...
//	}
func (e *ErrSuspended) WithSuspendTTL(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ttlTimer != nil {
		e.ttlTimer.Stop()
	}
	e.ttlTimer = time.AfterFunc(d, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.resume != nil && e.onRelease != nil {
			e.onRelease(e.snapshotSize)
			e.onRelease = nil
		}
		e.resume = nil
		e.resumeStream = nil
	})
}

// StepSuspended is re-exported from the workflow package.
const StepSuspended = workflow.StepSuspended

// resumeDataKey is the reserved WorkflowContext key for resume data.
// Must match workflow.resumeDataKey.
const resumeDataKey = "_resume_data"

// ResumeData retrieves resume data from the WorkflowContext.
// Returns the data and true if this step is being resumed, or nil and false
// on first execution. Safe to call with a nil WorkflowContext (returns nil, false).
func ResumeData(wCtx *WorkflowContext) (json.RawMessage, bool) {
	if wCtx == nil {
		return nil, false
	}
	v, ok := wCtx.Get(resumeDataKey)
	if !ok {
		return nil, false
	}
	data, ok := v.(json.RawMessage)
	return data, ok
}

// estimateSnapshotSize returns a rough byte count for a message slice.
// Counts Content, ToolCall Args/Metadata, and message-level Metadata.
// Attachment.Data is shared (not deep-copied), so excluded.
func estimateSnapshotSize(messages []core.ChatMessage) int64 {
	var size int64
	for _, m := range messages {
		size += int64(len(m.Content))
		for _, tc := range m.ToolCalls {
			size += int64(len(tc.Args))
			size += int64(len(tc.Metadata))
		}
		size += int64(len(m.Metadata))
	}
	return size
}

// checkSuspendLoop checks if a processor error is a suspend signal.
// Returns a fully-wired ErrSuspended (with resume closure) if it is, nil otherwise.
// The resume closure captures the current conversation messages, appends the
// human's response, and re-enters runLoop.
//
// A default TTL of 30 minutes is applied automatically. Callers can override
// with WithSuspendTTL or call Release() explicitly.
func checkSuspendLoop(err error, cfg LoopConfig, messages []core.ChatMessage, task AgentTask) *ErrSuspended {
	var suspend *errSuspend
	if !errors.As(err, &suspend) {
		return nil
	}

	// Compute snapshot size once for both budget check and ErrSuspended.
	snapSize := estimateSnapshotSize(messages)

	// Enforce per-agent suspend budget.
	// The check-then-add must be atomic to prevent concurrent suspensions
	// from both passing the check and exceeding the budget (TOCTOU race).
	if cfg.SuspendCount != nil {
		maxSnap := cfg.MaxSuspendSnapshots
		if maxSnap <= 0 {
			maxSnap = defaultMaxSuspendSnapshots
		}
		maxBytes := cfg.MaxSuspendBytes
		if maxBytes <= 0 {
			maxBytes = defaultMaxSuspendBytes
		}
		cfg.SuspendMu.Lock()
		count := *cfg.SuspendCount
		bytes := *cfg.SuspendBytes
		if count >= int64(maxSnap) || bytes+snapSize > maxBytes {
			cfg.SuspendMu.Unlock()
			cfg.Logger.Warn("suspend budget exceeded, skipping suspension",
				"agent", cfg.Name,
				"count", count,
				"bytes", bytes)
			return nil // caller propagates the original processor error
		}
		*cfg.SuspendCount = count + 1
		*cfg.SuspendBytes = bytes + snapSize
		cfg.SuspendMu.Unlock()
	}

	// Deep-copy messages for resume closure so that ToolCalls, Attachments,
	// and Metadata slices don't share backing arrays with the original.
	// Inner byte slices (ToolCall.Args/Metadata, Attachment.Data) are also
	// deep-copied to prevent shared mutable state across the snapshot boundary.
	snapshot := make([]core.ChatMessage, len(messages))
	for i, m := range messages {
		snapshot[i] = m
		if len(m.ToolCalls) > 0 {
			snapshot[i].ToolCalls = make([]core.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				snapshot[i].ToolCalls[j] = tc
				if len(tc.Args) > 0 {
					snapshot[i].ToolCalls[j].Args = make(json.RawMessage, len(tc.Args))
					copy(snapshot[i].ToolCalls[j].Args, tc.Args)
				}
				if len(tc.Metadata) > 0 {
					snapshot[i].ToolCalls[j].Metadata = make(json.RawMessage, len(tc.Metadata))
					copy(snapshot[i].ToolCalls[j].Metadata, tc.Metadata)
				}
			}
		}
		// Isolate the Attachments slice header so mutations to the original
		// (append, reorder) don't affect the snapshot. Attachment.Data is
		// treated as immutable throughout the framework, so sharing the
		// backing byte slice is safe and avoids duplicating large binary
		// content (images, PDFs, audio).
		if len(m.Attachments) > 0 {
			snapshot[i].Attachments = make([]core.Attachment, len(m.Attachments))
			copy(snapshot[i].Attachments, m.Attachments)
		}
		if len(m.Metadata) > 0 {
			snapshot[i].Metadata = make(json.RawMessage, len(m.Metadata))
			copy(snapshot[i].Metadata, m.Metadata)
		}
	}

	formatFn := suspend.format
	if formatFn == nil {
		formatFn = defaultResumeFormat
	}

	suspended := &ErrSuspended{
		Step:         cfg.Name,
		Payload:      suspend.payload,
		tag:          suspend.tag, // propagate from sentinel
		snapshotSize: snapSize,
		resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
			resumed := make([]core.ChatMessage, len(snapshot)+1)
			copy(resumed, snapshot)
			resumed[len(snapshot)] = core.UserMessage(formatFn(data))
			resumeCfg := cfg
			resumeCfg.ResumeMessages = resumed
			return runLoop(ctx, resumeCfg, task, nil)
		},
		resumeStream: func(ctx context.Context, data json.RawMessage, ch chan<- core.StreamEvent) (AgentResult, error) {
			// runLoop closes ch via its safeCloseCh — no additional defer close here.
			resumed := make([]core.ChatMessage, len(snapshot)+1)
			copy(resumed, snapshot)
			resumed[len(snapshot)] = core.UserMessage(formatFn(data))
			resumeCfg := cfg
			resumeCfg.ResumeMessages = resumed
			return runLoop(ctx, resumeCfg, task, ch)
		},
	}
	if cfg.SuspendCount != nil {
		suspended.onRelease = func(size int64) {
			cfg.SuspendMu.Lock()
			*cfg.SuspendCount--
			*cfg.SuspendBytes -= size
			cfg.SuspendMu.Unlock()
		}
	}
	// Apply default TTL to prevent memory leaks from abandoned suspensions.
	// Callers can override with WithSuspendTTL(d) where d > 0.
	suspended.WithSuspendTTL(defaultSuspendTTL)
	return suspended
}
