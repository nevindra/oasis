package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nevindra/oasis/history"
	"github.com/nevindra/oasis/memory"
)

func TestRunOptions_NilValidates(t *testing.T) {
	// A nil *RunOptions is equivalent to "no overrides" and must pass validation.
	if err := (*RunOptions)(nil).Validate(); err != nil {
		t.Fatalf("nil RunOptions: Validate = %v, want nil", err)
	}
}

func TestRunOptions_EmptyValidates(t *testing.T) {
	if err := (&RunOptions{}).Validate(); err != nil {
		t.Fatalf("empty RunOptions: Validate = %v, want nil", err)
	}
}

func TestRunOptions_PositiveMaxIterValidates(t *testing.T) {
	n := 5
	opts := &RunOptions{MaxIter: &n}
	if err := opts.Validate(); err != nil {
		t.Fatalf("MaxIter=5: Validate = %v, want nil", err)
	}
}

func TestRunOptions_ZeroMaxIterFails(t *testing.T) {
	n := 0
	opts := &RunOptions{MaxIter: &n}
	err := opts.Validate()
	if err == nil {
		t.Fatalf("MaxIter=0: Validate = nil, want error")
	}
	var roErr *RunOptionsError
	if !errors.As(err, &roErr) {
		t.Fatalf("MaxIter=0: error is not *RunOptionsError: %v", err)
	}
	if roErr.Field != "MaxIter" {
		t.Fatalf("MaxIter=0: Field = %q, want %q", roErr.Field, "MaxIter")
	}
}

func TestRunOptions_NegativeMaxIterFails(t *testing.T) {
	n := -1
	opts := &RunOptions{MaxIter: &n}
	if err := opts.Validate(); err == nil {
		t.Fatalf("MaxIter=-1: Validate = nil, want error")
	}
}

func TestRunOptions_ZeroMaxAttachmentBytesFails(t *testing.T) {
	var n int64 = 0
	opts := &RunOptions{MaxAttachmentBytes: &n}
	if err := opts.Validate(); err == nil {
		t.Fatalf("MaxAttachmentBytes=0: Validate = nil, want error")
	}
}

func TestRunOptions_HasOverrides_Empty(t *testing.T) {
	if (&RunOptions{}).HasOverrides() {
		t.Fatalf("empty RunOptions: HasOverrides = true, want false")
	}
}

func TestRunOptions_HasOverrides_NilIsFalse(t *testing.T) {
	if (*RunOptions)(nil).HasOverrides() {
		t.Fatalf("nil RunOptions: HasOverrides = true, want false")
	}
}

func TestRunOptions_HasOverrides_MaxIterSet(t *testing.T) {
	n := 5
	opts := &RunOptions{MaxIter: &n}
	if !opts.HasOverrides() {
		t.Fatalf("RunOptions{MaxIter: &5}: HasOverrides = false, want true")
	}
}

func TestApplyRunOptions_NilNoChange(t *testing.T) {
	base := &Config{maxIter: 10}
	out := applyRunOptions(base, nil)
	if out != base {
		t.Fatalf("nil opts: applyRunOptions returned different config")
	}
	if out.maxIter != 10 {
		t.Fatalf("nil opts: maxIter changed to %d", out.maxIter)
	}
}

func TestApplyRunOptions_EmptyNoChange(t *testing.T) {
	base := &Config{maxIter: 10}
	out := applyRunOptions(base, &RunOptions{})
	if out != base {
		t.Fatalf("empty opts: applyRunOptions returned different config")
	}
}

func TestApplyRunOptions_MaxIterOverride(t *testing.T) {
	base := &Config{maxIter: 10}
	n := 3
	out := applyRunOptions(base, &RunOptions{MaxIter: &n})
	if out == base {
		t.Fatalf("non-nil opts: applyRunOptions did not return a copy")
	}
	if out.maxIter != 3 {
		t.Fatalf("MaxIter override: got %d, want 3", out.maxIter)
	}
	if base.maxIter != 10 {
		t.Fatalf("MaxIter override leaked into base: %d", base.maxIter)
	}
}

func TestApplyRunOptions_PromptOverride(t *testing.T) {
	base := &Config{prompt: "agent-default"}
	override := "call-override"
	out := applyRunOptions(base, &RunOptions{Prompt: &override})
	if out.prompt != "call-override" {
		t.Fatalf("Prompt override: got %q, want %q", out.prompt, "call-override")
	}
	if base.prompt != "agent-default" {
		t.Fatalf("Prompt override leaked into base: %q", base.prompt)
	}
}

func TestApplyRunOptions_GenerationPartialMerge(t *testing.T) {
	temp := 0.3
	topP := 0.9
	base := &Config{generationParams: &GenerationParams{Temperature: &temp, TopP: &topP}}

	newTemp := 0.7
	out := applyRunOptions(base, &RunOptions{Generation: &Generation{Temperature: &newTemp}})

	if out.generationParams == nil {
		t.Fatalf("Generation partial: generationParams nil after override")
	}
	if out.generationParams.Temperature == nil || *out.generationParams.Temperature != 0.7 {
		t.Fatalf("Generation partial: Temperature = %v, want 0.7", out.generationParams.Temperature)
	}
	if out.generationParams.TopP == nil || *out.generationParams.TopP != 0.9 {
		t.Fatalf("Generation partial: TopP = %v, want 0.9 (preserved)", out.generationParams.TopP)
	}
	// Base must not be mutated
	if base.generationParams.Temperature == nil || *base.generationParams.Temperature != 0.3 {
		t.Fatalf("Generation partial: base mutated to %v, want 0.3", base.generationParams.Temperature)
	}
}

func TestApplyRunOptions_HookPrecedence(t *testing.T) {
	baseHook := func(ctx context.Context, iter int, ctrl *StepControl) error { return nil }
	overrideHook := func(ctx context.Context, iter int, ctrl *StepControl) error { return nil }

	base := &Config{prepareStep: baseHook}
	out := applyRunOptions(base, &RunOptions{PrepareStep: overrideHook})

	if reflect.ValueOf(out.prepareStep).Pointer() != reflect.ValueOf(overrideHook).Pointer() {
		t.Fatalf("PrepareStep precedence: RunOptions hook did not win")
	}
}

func TestApplyRunOptions_MetadataMerge(t *testing.T) {
	base := &Config{metadata: map[string]any{"a": 1, "b": 2}}
	out := applyRunOptions(base, &RunOptions{Metadata: map[string]any{"b": 99, "c": 3}})

	want := map[string]any{"a": 1, "b": 99, "c": 3}
	if !reflect.DeepEqual(out.metadata, want) {
		t.Fatalf("Metadata merge: got %v, want %v", out.metadata, want)
	}
	// Base must not be mutated
	if !reflect.DeepEqual(base.metadata, map[string]any{"a": 1, "b": 2}) {
		t.Fatalf("Metadata merge: base mutated to %v", base.metadata)
	}
}

func TestWithMetadata(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithMetadata(map[string]any{"key1": "v1", "key2": 2})})
	if cfg.metadata["key1"] != "v1" || cfg.metadata["key2"] != 2 {
		t.Fatalf("WithMetadata: cfg.metadata = %v, want {key1:v1, key2:2}", cfg.metadata)
	}
}

func TestRunOptions_MemoryOverride_PerCall(t *testing.T) {
	// Construct two memory orchestrators backed by distinct recordingStores.
	// Calling ExecuteWith with the override memory should route persistence
	// writes to the override store, not the agent default store.

	defaultStore := &recordingStore{}
	overrideStore := &recordingStore{}

	defaultMem := &memory.AgentMemory{}
	defaultMem.Init(memory.AgentMemoryConfig{
		Store: defaultStore,
	})

	overrideMem := &memory.AgentMemory{}
	overrideMem.Init(memory.AgentMemoryConfig{
		Store: overrideStore,
	})

	a := NewLLMAgent("a", "d", &capturedRequestProvider{},
		WithHistory(history.Store(defaultStore)),
	)

	_, err := a.ExecuteWith(context.Background(),
		AgentTask{Input: "hello tenant", ThreadID: "tenant-acme"},
		&RunOptions{Memory: overrideMem})
	if err != nil {
		t.Fatalf("ExecuteWith: %v", err)
	}

	// Wait for background persist goroutines to complete.
	time.Sleep(100 * time.Millisecond)

	// Override store should have received persistence calls; default store should not.
	defaultWrites := len(defaultStore.storedMessages())
	overrideWrites := len(overrideStore.storedMessages())

	if defaultWrites > 0 {
		t.Fatalf("default store received %d writes — should be 0 (override used)", defaultWrites)
	}
	if overrideWrites == 0 {
		t.Fatalf("override store received 0 writes — should be > 0")
	}
}
