// memory/ingest_test.go
package memory

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

type captureProc struct {
	called       bool
	addCandidate core.MemoryItem
}

func (p *captureProc) Process(_ context.Context, in *IngestContext) error {
	p.called = true
	if p.addCandidate.ID != "" {
		in.Candidates = append(in.Candidates, p.addCandidate)
	}
	return nil
}

func TestRunIngestPipeline_CallsProcessorsInOrder(t *testing.T) {
	p1 := &captureProc{addCandidate: core.MemoryItem{ID: "a", Kind: KindFact, Content: "fact a"}}
	p2 := &captureProc{}
	ctx := &IngestContext{}
	err := runIngestPipeline(context.Background(), ctx, []IngestProcessor{p1, p2})
	if err != nil {
		t.Fatal(err)
	}
	if !p1.called || !p2.called {
		t.Fatal("processors not called")
	}
	if len(ctx.Candidates) != 1 || ctx.Candidates[0].ID != "a" {
		t.Fatalf("candidates not propagated: %+v", ctx.Candidates)
	}
}

func TestRunIngestPipeline_StopsOnError(t *testing.T) {
	failing := &errorProc{err: context.Canceled}
	after := &captureProc{}
	err := runIngestPipeline(context.Background(), &IngestContext{}, []IngestProcessor{failing, after})
	if err == nil {
		t.Fatal("expected error")
	}
	if after.called {
		t.Fatal("processor after error should not run")
	}
}

type errorProc struct{ err error }

func (p *errorProc) Process(context.Context, *IngestContext) error { return p.err }

// noopIngest provides a minimal IngestContext for the test.
var _ = core.NewID
