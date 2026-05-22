// Package agent — typed suspend/resume contracts.
//
// SuspendProtocol[Req, Resp] is a typed handle that pins the payload type
// (Req) sent to the human and the response type (Resp) sent back, declared
// once and referenced from both the suspending site and the caller that
// resumes. The framework primitives ErrSuspended, Agent, Workflow, and
// Network stay monomorphic — the generic parameters live only on the
// protocol value and its methods, captured into closures at registration.
//
// See docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md
// for the full design.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// SuspendProtocol is a typed HITL contract. Declare once with
// NewSuspendProtocol, then use its methods to suspend (from a workflow
// step or processor) and resume (from the caller that receives
// *ErrSuspended). The zero value is not usable.
type SuspendProtocol[Req, Resp any] struct {
	name         string
	renderResume func(Resp) string
}

// NewSuspendProtocol declares a typed HITL contract. The name is a stable
// identifier used in error messages and the runtime tag check that
// catches "wrong protocol used to resume" mistakes. Names should be
// unique within a process; the framework does not enforce uniqueness.
//
// By convention, namespace protocol names with a domain prefix to avoid
// collisions across packages, e.g. "billing.approve_transfer".
func NewSuspendProtocol[Req, Resp any](name string) SuspendProtocol[Req, Resp] {
	return SuspendProtocol[Req, Resp]{name: name}
}

// Name returns the protocol's stable identifier.
func (p SuspendProtocol[Req, Resp]) Name() string { return p.name }

// WithRenderResume sets a formatter that converts the typed resume data
// into the natural-language message injected into the LLM's conversation
// history. When not set, the default formatter produces
// "Human resumed `<name>`: <json>".
//
// Returns the protocol so calls can chain at declaration time:
//
//	var ApproveTransfer = oasis.NewSuspendProtocol[Req, Resp]("name").
//	    WithRenderResume(func(r Resp) string { ... })
func (p SuspendProtocol[Req, Resp]) WithRenderResume(fn func(Resp) string) SuspendProtocol[Req, Resp] {
	p.renderResume = fn
	return p
}

// formatBytes is the type-erased formatter used by the resume closure.
// It unmarshals into Resp and applies renderResume if set, otherwise
// returns the tagged-JSON default. On unmarshal failure it falls back
// to the tagged-JSON default so the LLM still gets readable context
// instead of a crash.
func (p SuspendProtocol[Req, Resp]) formatBytes(data json.RawMessage) string {
	if p.renderResume != nil {
		var resp Resp
		if err := json.Unmarshal(data, &resp); err == nil {
			return p.renderResume(resp)
		}
		// fall through to tagged-JSON default on unmarshal failure
	}
	return fmt.Sprintf("Human resumed `%s`: %s", p.name, string(data))
}

// Suspend returns an error that signals the engine to pause execution.
// The payload is JSON-marshaled with the protocol's tag and formatter
// attached, so any caller using the same protocol value can read the
// payload typed (via PayloadFrom) and resume typed (via Resume).
//
// Returns a non-suspend error if marshaling fails — propagate it as
// normal. Tools should not invoke Suspend directly; suspend from a
// workflow step or processor whose return type is error.
func (p SuspendProtocol[Req, Resp]) Suspend(payload Req) error {
	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("SuspendProtocol[%s].Suspend: marshal payload: %w", p.name, err)
	}
	return &errSuspend{
		payload: bytes,
		tag:     p.name,
		format:  p.formatBytes,
	}
}

// PayloadFrom reads the suspended payload as the typed Req.
// Returns an error if e is nil, has a different protocol tag than this
// protocol, or the payload bytes don't unmarshal as Req.
func (p SuspendProtocol[Req, Resp]) PayloadFrom(e *ErrSuspended) (Req, error) {
	var zero Req
	if e == nil {
		return zero, errors.New("PayloadFrom: nil suspended err")
	}
	if e.tag != p.name {
		return zero, fmt.Errorf(
			"PayloadFrom: protocol mismatch: suspended with %q, queried with %q",
			tagDescriptor(e.tag), p.name,
		)
	}
	var out Req
	if err := json.Unmarshal(e.Payload, &out); err != nil {
		return zero, fmt.Errorf("PayloadFrom: unmarshal payload as %T: %w", out, err)
	}
	return out, nil
}

// Resume continues execution with the typed response data. The data is
// JSON-marshaled and handed to the engine's existing untyped resume path;
// the protocol's formatter (set via WithRenderResume, or the default
// tagged-JSON formatter) shapes the message the LLM sees.
//
// Returns an error if e is nil, has a different protocol tag, or any
// error the underlying (*ErrSuspended).Resume would return (released,
// expired, marshal failure on data, etc.).
func (p SuspendProtocol[Req, Resp]) Resume(e *ErrSuspended, ctx context.Context, data Resp) (AgentResult, error) {
	if e == nil {
		return AgentResult{}, errors.New("Resume: nil suspended err")
	}
	if e.tag != p.name {
		return AgentResult{}, fmt.Errorf(
			"Resume: protocol mismatch: suspended with %q, attempted via %q",
			tagDescriptor(e.tag), p.name,
		)
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return AgentResult{}, fmt.Errorf("Resume: marshal data: %w", err)
	}
	return e.Resume(ctx, bytes)
}

// ResumeStream is the streaming form of Resume. Same tag check, same
// JSON marshaling; events are emitted on ch by the engine throughout
// the post-resume loop. The engine closes ch when streaming completes.
func (p SuspendProtocol[Req, Resp]) ResumeStream(e *ErrSuspended, ctx context.Context, data Resp, ch chan<- StreamEvent) (AgentResult, error) {
	if e == nil {
		return AgentResult{}, errors.New("ResumeStream: nil suspended err")
	}
	if e.tag != p.name {
		return AgentResult{}, fmt.Errorf(
			"ResumeStream: protocol mismatch: suspended with %q, attempted via %q",
			tagDescriptor(e.tag), p.name,
		)
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return AgentResult{}, fmt.Errorf("ResumeStream: marshal data: %w", err)
	}
	return e.ResumeStream(ctx, bytes, ch)
}

// tagDescriptor returns a human-readable label for an ErrSuspended's
// internal tag, mapping the empty tag to the literal token "<untyped>"
// so mismatch errors are unambiguous when one side used the untyped path.
func tagDescriptor(tag string) string {
	if tag == "" {
		return "<untyped>"
	}
	return tag
}
