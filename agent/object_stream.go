package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	"github.com/nevindra/oasis/core"
)

// newObjectStreamForwarder is like newCapturingStreamForwarder but also emits
// EventObjectDelta snapshots (via core.PartialJSON) as text deltas arrive when
// cfg.responseSchema is set. For top-level array schemas it additionally emits
// EventElementDelta once per completed array element.
//
// Returns (iterCh, wait). Callers pass iterCh to the provider and MUST call
// wait() after the provider returns to ensure the forwarder finishes draining.
//
// When dest is nil (non-streaming Execute path), falls back to
// newCapturingStreamForwarder (which no-ops on nil dest).
func newObjectStreamForwarder(ctx context.Context, dest chan<- StreamEvent, bufSize int, state *loopState, schema *ResponseSchema) (chan<- StreamEvent, func()) {
	if dest == nil || schema == nil {
		return newCapturingStreamForwarder(ctx, dest, bufSize, state)
	}

	// Detect whether the schema's top-level type is "array".
	isArraySchema := false
	{
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(schema.Schema, &probe)
		isArraySchema = probe.Type == "array"
	}

	iterCh := make(chan StreamEvent, bufSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		var (
			buf      []byte   // accumulates text deltas
			lastEmit []byte   // last snapshot sent as EventObjectDelta (for dedup)
			elemTracker *elementTracker // non-nil only for top-level array schemas
		)
		if isArraySchema {
			elemTracker = newElementTracker()
		}

		for ev := range iterCh {
			// Capture file attachments (same as newCapturingStreamForwarder).
			captureFileEvent(ev, state)

			if ev.Type == EventTextDelta {
				buf = append(buf, ev.Content...)

				if isArraySchema && elemTracker != nil {
					// Feed new bytes to element tracker and emit any completed elements.
					newElems := elemTracker.feed(buf)
					for _, elemBytes := range newElems {
						select {
						case dest <- StreamEvent{Type: EventElementDelta, Object: elemBytes}:
						case <-ctx.Done():
							// drain and exit
							for range iterCh {
							}
							return
						}
					}
				}

				// Emit EventObjectDelta snapshot (deduplicated).
				if snap, ok := core.PartialJSON(buf); ok && !bytes.Equal(snap, lastEmit) {
					lastEmit = append(lastEmit[:0], snap...)
					select {
					case dest <- StreamEvent{Type: EventObjectDelta, Object: snap}:
					case <-ctx.Done():
						for range iterCh {
						}
						return
					}
				}
			}

			// Forward the original event.
			select {
			case dest <- ev:
			case <-ctx.Done():
				for range iterCh {
				}
				return
			}
		}
	}()
	return iterCh, wg.Wait
}

// emitObjectFinish emits an EventObjectFinish event and populates result.Object
// when the schema is configured and resp.Content is valid JSON.
func emitObjectFinish(ctx context.Context, ch chan<- StreamEvent, schema *ResponseSchema, content string, result *AgentResult) {
	if ch == nil || schema == nil || len(content) == 0 {
		return
	}
	b := []byte(content)
	if !json.Valid(b) {
		return
	}
	result.Object = b
	select {
	case ch <- StreamEvent{Type: EventObjectFinish, Object: b}:
	case <-ctx.Done():
	}
}

// elementTracker detects completed top-level array elements in a streaming
// JSON buffer. It tracks brace/bracket depth (skipping inside strings) and
// fires once per element as it closes at depth 1 (inside the top-level array).
//
// Call feed(buf) with the full accumulated buffer each time new bytes arrive.
// It remembers how far it has scanned and returns any newly completed element
// byte slices (ready to JSON-unmarshal individually).
type elementTracker struct {
	scanned  int  // bytes already processed in previous calls
	depth    int  // current nesting depth
	inString bool // currently inside a JSON string
	escape   bool // last char was backslash inside a string
	elemStart int  // byte offset where the current element started (-1 if none)
}

func newElementTracker() *elementTracker {
	return &elementTracker{elemStart: -1}
}

// feed processes bytes from buf[t.scanned:] and returns slices (from buf) for
// any newly completed top-level array elements. The returned slices are valid
// subslices of buf — callers should copy them if they need long-lived data.
func (t *elementTracker) feed(buf []byte) []json.RawMessage {
	var completed []json.RawMessage

	for i := t.scanned; i < len(buf); i++ {
		b := buf[i]

		if t.inString {
			if t.escape {
				t.escape = false
				continue
			}
			if b == '\\' {
				t.escape = true
				continue
			}
			if b == '"' {
				t.inString = false
			}
			continue
		}

		switch b {
		case '"':
			t.inString = true
		case '{', '[':
			if t.depth == 1 && t.elemStart == -1 {
				// Start of a new element inside the top-level array.
				t.elemStart = i
			}
			t.depth++
		case '}', ']':
			t.depth--
			if t.depth == 1 && t.elemStart != -1 {
				// Completed an element at depth 1.
				elem := make([]byte, i+1-t.elemStart)
				copy(elem, buf[t.elemStart:i+1])
				completed = append(completed, json.RawMessage(elem))
				t.elemStart = -1
			}
			if t.depth == 0 {
				// Closed the top-level array — done.
				t.scanned = i + 1
				return completed
			}
		case ',':
			// At depth 1, commas separate elements. Scalar elements (strings,
			// numbers, booleans) would have already closed at depth reduction;
			// this handles literal/scalar top-level elements if they exist.
			if t.depth == 1 && t.elemStart != -1 {
				// Scalar element ended.
				elem := make([]byte, i-t.elemStart)
				copy(elem, buf[t.elemStart:i])
				// Trim trailing whitespace.
				trimmed := bytes.TrimRight(elem, " \t\n\r")
				if len(trimmed) > 0 && json.Valid(trimmed) {
					completed = append(completed, json.RawMessage(trimmed))
				}
				t.elemStart = -1
			} else if t.depth == 1 && t.elemStart == -1 {
				// Between elements at depth 1 — nothing to do.
			}
		default:
			// Non-whitespace, non-structure character at depth 1 = start of
			// scalar element (string already handled by '"' case above).
			if t.depth == 1 && t.elemStart == -1 && b != ' ' && b != '\t' && b != '\n' && b != '\r' {
				t.elemStart = i
			}
		}
	}
	t.scanned = len(buf)
	return completed
}
