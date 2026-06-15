package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

func collect(t *testing.T, src []core.StreamEvent, hook func(context.Context, core.StreamEvent) (*core.StreamEvent, error)) []core.StreamEvent {
	t.Helper()
	dest := make(chan core.StreamEvent, 16)
	in, wait := newForwarder(context.Background(), dest, 16, forwarderConfig{onChunk: hook})
	for _, ev := range src {
		in <- ev
	}
	close(in)
	wait()
	close(dest)
	var out []core.StreamEvent
	for ev := range dest {
		out = append(out, ev)
	}
	return out
}

func TestForwarderChunkMutate(t *testing.T) {
	hook := func(_ context.Context, ev core.StreamEvent) (*core.StreamEvent, error) {
		ev.Content = strings.ToUpper(ev.Content)
		return &ev, nil
	}
	out := collect(t, []core.StreamEvent{{Type: core.EventTextDelta, Content: "hi"}}, hook)
	if len(out) != 1 || out[0].Content != "HI" {
		t.Errorf("expected mutated HI, got %+v", out)
	}
}

func TestForwarderChunkDrop(t *testing.T) {
	hook := func(_ context.Context, ev core.StreamEvent) (*core.StreamEvent, error) {
		return nil, nil // drop all
	}
	out := collect(t, []core.StreamEvent{{Type: core.EventTextDelta, Content: "secret"}}, hook)
	if len(out) != 0 {
		t.Errorf("expected dropped, got %+v", out)
	}
}

func TestForwarderChunkHalt(t *testing.T) {
	hook := func(_ context.Context, ev core.StreamEvent) (*core.StreamEvent, error) {
		return nil, &core.ErrHalt{Response: "stop"}
	}
	out := collect(t, []core.StreamEvent{{Type: core.EventTextDelta, Content: "boom"}}, hook)
	if len(out) != 1 || out[0].Type != core.EventHalt || out[0].Content != "stop" {
		t.Errorf("expected single EventHalt 'stop', got %+v", out)
	}
}
