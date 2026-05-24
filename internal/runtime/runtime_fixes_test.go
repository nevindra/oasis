package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

// --- minimal test processors ---

type recordingPreProcessor struct{ id string }

func (r *recordingPreProcessor) PreLLM(_ context.Context, _ *core.ChatRequest) error { return nil }

type recordingPostProcessor struct{ id string }

func (r *recordingPostProcessor) PostLLM(_ context.Context, _ *core.ChatResponse) error { return nil }

type recordingPostToolProcessor struct{ id string }

func (r *recordingPostToolProcessor) PostTool(_ context.Context, _ core.ToolCall, _ *core.ToolResult) error {
	return nil
}

// TestApplyRunOptionsToConfig_AppendsProcessors verifies that per-call
// processors supplied via RunOptions are appended to (not replacing) the
// agent-level processor chain, mirroring Processors.ApplyTo semantics.
func TestApplyRunOptionsToConfig_AppendsProcessors(t *testing.T) {
	baseline := &recordingPreProcessor{id: "base-pre"}
	callPre := &recordingPreProcessor{id: "call-pre"}

	baselinePost := &recordingPostProcessor{id: "base-post"}
	callPost := &recordingPostProcessor{id: "call-post"}

	baselinePostTool := &recordingPostToolProcessor{id: "base-post-tool"}
	callPostTool := &recordingPostToolProcessor{id: "call-post-tool"}

	base := &Config{
		PreProcessors:      []core.PreProcessor{baseline},
		PostProcessors:     []core.PostProcessor{baselinePost},
		PostToolProcessors: []core.PostToolProcessor{baselinePostTool},
	}

	opts := &RunOptions{
		PreProcessors:      []core.PreProcessor{callPre},
		PostProcessors:     []core.PostProcessor{callPost},
		PostToolProcessors: []core.PostToolProcessor{callPostTool},
	}

	got := ApplyRunOptionsToConfig(base, opts)

	// PreProcessors: both processors must be present in order.
	if len(got.PreProcessors) != 2 {
		t.Fatalf("PreProcessors: want 2 entries, got %d", len(got.PreProcessors))
	}
	if got.PreProcessors[0] != baseline {
		t.Errorf("PreProcessors[0]: want %q, got something else", "base-pre")
	}
	if got.PreProcessors[1] != callPre {
		t.Errorf("PreProcessors[1]: want %q, got something else", "call-pre")
	}

	// PostProcessors: both must be present in order.
	if len(got.PostProcessors) != 2 {
		t.Fatalf("PostProcessors: want 2 entries, got %d", len(got.PostProcessors))
	}
	if got.PostProcessors[0] != baselinePost {
		t.Errorf("PostProcessors[0]: want base-post")
	}
	if got.PostProcessors[1] != callPost {
		t.Errorf("PostProcessors[1]: want call-post")
	}

	// PostToolProcessors: both must be present in order.
	if len(got.PostToolProcessors) != 2 {
		t.Fatalf("PostToolProcessors: want 2 entries, got %d", len(got.PostToolProcessors))
	}
	if got.PostToolProcessors[0] != baselinePostTool {
		t.Errorf("PostToolProcessors[0]: want base-post-tool")
	}
	if got.PostToolProcessors[1] != callPostTool {
		t.Errorf("PostToolProcessors[1]: want call-post-tool")
	}

	// Baseline Config must not have been mutated.
	if len(base.PreProcessors) != 1 {
		t.Errorf("base.PreProcessors was mutated: got %d entries", len(base.PreProcessors))
	}
	if len(base.PostProcessors) != 1 {
		t.Errorf("base.PostProcessors was mutated: got %d entries", len(base.PostProcessors))
	}
	if len(base.PostToolProcessors) != 1 {
		t.Errorf("base.PostToolProcessors was mutated: got %d entries", len(base.PostToolProcessors))
	}
}

// TestEffectiveToolMiddleware_NoSliceAliasing verifies that concurrent calls
// to effectiveToolMiddleware never write to the same slot in Config.ToolMiddleware.
// Run with: go test -race ./internal/runtime/...
func TestEffectiveToolMiddleware_NoSliceAliasing(t *testing.T) {
	// Build a slice with len=1, cap=8 to maximise the chance that a naive
	// `mws := c.Config.ToolMiddleware; append(mws, ...)` aliases the backing
	// array across goroutines.
	identity := core.ToolMiddleware(func(t core.AnyTool) core.AnyTool { return t })
	base := make([]core.ToolMiddleware, 1, 8)
	base[0] = identity

	rt := &Runtime{
		Config: Config{
			ToolMiddleware: base,
			// No Tracer, no ToolApprovals — we just need the copy path exercised.
		},
	}

	const goroutines = 32
	var wg sync.WaitGroup
	results := make([][]core.ToolMiddleware, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i] = rt.effectiveToolMiddleware()
		}()
	}
	wg.Wait()

	// Every result must contain exactly the one base middleware.
	for i, res := range results {
		if len(res) != 1 {
			t.Errorf("goroutine %d: want 1 middleware, got %d", i, len(res))
		}
	}
}
