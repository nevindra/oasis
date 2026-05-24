package oasis_test

import (
	"context"
	"testing"

	"github.com/nevindra/oasis"
	"github.com/nevindra/oasis/core"
)

// nopAgent satisfies oasis.Agent with a fixed result.
type nopAgent struct{}

func (n *nopAgent) Name() string        { return "nop" }
func (n *nopAgent) Description() string { return "" }
func (n *nopAgent) Execute(_ context.Context, _ oasis.AgentTask, opts ...core.RunOption) (oasis.AgentResult, error) {
	cfg := core.ApplyRunOptions(opts...)
	if cfg.Stream != nil {
		close(cfg.Stream)
	}
	return oasis.AgentResult{Output: "hi"}, nil
}

// TestOasis_Subscribe exercises the curated [oasis.Subscribe] re-export and
// confirms Stream.Text() returns the final result.
func TestOasis_Subscribe(t *testing.T) {
	s := oasis.Subscribe(context.Background(), &nopAgent{}, oasis.AgentTask{})
	if got := s.Text(); got != "hi" {
		t.Errorf("Text() = %q, want %q", got, "hi")
	}
}
