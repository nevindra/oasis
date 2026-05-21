package agent

import (
	"encoding/json"
	"errors"

	"github.com/nevindra/oasis/core"
)

// ResultObjectAs decodes AgentResult.Object into a typed T. Use after
// running an agent with WithResponseSchema to get a typed final result.
//
//	type Report struct {
//	    Title string `json:"title"`
//	}
//	result, _ := agent.Execute(ctx, task)
//	report, err := agent.ResultObjectAs[Report](result)
//
// Returns an error when r.Object is empty (agent had no schema, or the
// model produced an unparseable response) or when the JSON does not
// decode into T.
func ResultObjectAs[T any](r AgentResult) (T, error) {
	var zero T
	if len(r.Object) == 0 {
		return zero, errors.New("oasis: AgentResult.Object is empty (no schema configured, or no structured output produced)")
	}
	var v T
	if err := json.Unmarshal(r.Object, &v); err != nil {
		return zero, err
	}
	return v, nil
}

// StreamObjectAs subscribes to a Stream and forwards each EventObjectDelta
// (and the final EventObjectFinish / EventElementDelta) as a typed T value.
// The returned channel closes when the underlying stream finishes.
//
// Internally allocates one goroutine that reads from the stream's fan-out
// wrapper and decodes each snapshot. Failed decodes (a snapshot that doesn't
// fit T yet) are silently dropped — the next valid snapshot supersedes.
// The final EventObjectFinish always produces a successful decode (or the run
// ended without structured output, in which case the channel just closes).
//
//	for partial := range agent.StreamObjectAs[Report](stream) {
//	    ui.Render(partial)
//	}
func StreamObjectAs[T any](s *Stream) <-chan T {
	out := make(chan T, 8)
	go func() {
		defer close(out)
		evs := s.Events()
		for ev := range evs {
			if ev.Type != core.EventObjectDelta && ev.Type != core.EventObjectFinish && ev.Type != core.EventElementDelta {
				continue
			}
			var v T
			if err := json.Unmarshal(ev.Object, &v); err != nil {
				continue
			}
			out <- v
		}
	}()
	return out
}
