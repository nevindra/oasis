package agent

import (
	"context"
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


func TestApplyRunOptions_PromptOverride(t *testing.T) {
	base := &Config{systemPrompt: "agent-default"}
	override := "call-override"
	out := applyRunOptions(base, &RunOptions{Prompt: &override})
	if out.systemPrompt != "call-override" {
		t.Fatalf("Prompt override: got %q, want %q", out.systemPrompt, "call-override")
	}
	if base.systemPrompt != "agent-default" {
		t.Fatalf("Prompt override leaked into base: %q", base.systemPrompt)
	}
}

func TestApplyRunOptions_GenerationPartialMerge(t *testing.T) {
	temp := 0.3
	topP := 0.9
	base := &Config{genParams: &GenerationParams{Temperature: &temp, TopP: &topP}}

	newTemp := 0.7
	out := applyRunOptions(base, &RunOptions{Generation: &Generation{Temperature: &newTemp}})

	if out.genParams == nil {
		t.Fatalf("Generation partial: genParams nil after override")
	}
	if out.genParams.Temperature == nil || *out.genParams.Temperature != 0.7 {
		t.Fatalf("Generation partial: Temperature = %v, want 0.7", out.genParams.Temperature)
	}
	if out.genParams.TopP == nil || *out.genParams.TopP != 0.9 {
		t.Fatalf("Generation partial: TopP = %v, want 0.9 (preserved)", out.genParams.TopP)
	}
	// Base must not be mutated
	if base.genParams.Temperature == nil || *base.genParams.Temperature != 0.3 {
		t.Fatalf("Generation partial: base mutated to %v, want 0.3", base.genParams.Temperature)
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

func TestRunOptions_StreamReplayLimit_Valid(t *testing.T) {
	opts := &RunOptions{StreamReplayLimit: 128}
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestRunOptions_StreamReplayLimit_NegativeInvalid(t *testing.T) {
	opts := &RunOptions{StreamReplayLimit: -1}
	if err := opts.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for negative StreamReplayLimit")
	}
}

func TestRunOptions_StreamReplayLimit_ZeroOK(t *testing.T) {
	opts := &RunOptions{StreamReplayLimit: 0}
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (0 means default)", err)
	}
}

// TestRunOptions_LimitsOverride verifies that RunOptions.Limits replaces the
// agent's limits for the single Execute call.
func TestRunOptions_LimitsOverride(t *testing.T) {
	base := BuildConfig([]AgentOption{WithLimits(Limits{MaxIter: 25, MaxAttachmentBytes: 1000})})
	override := &Limits{MaxIter: 5, MaxAttachmentBytes: 2000}
	eff := applyRunOptions(base, &RunOptions{Limits: override})
	if eff.maxIter != 5 {
		t.Fatalf("MaxIter not overridden: got %d", eff.maxIter)
	}
	if eff.maxAttachmentBytes != 2000 {
		t.Fatalf("MaxAttachmentBytes not overridden: got %d", eff.maxAttachmentBytes)
	}
}

// TestRunOptions_LimitsZeroFieldKeepsBase verifies that zero fields on the
// Limits override preserve the base agent's values (partial-override
// semantics that match the WithLimits merge rules).
func TestRunOptions_LimitsZeroFieldKeepsBase(t *testing.T) {
	base := BuildConfig([]AgentOption{WithLimits(Limits{MaxIter: 25, MaxAttachmentBytes: 1000})})
	// Override only MaxIter; MaxAttachmentBytes stays at base 1000.
	eff := applyRunOptions(base, &RunOptions{Limits: &Limits{MaxIter: 5}})
	if eff.maxIter != 5 || eff.maxAttachmentBytes != 1000 {
		t.Fatalf("partial override broken: maxIter=%d maxAttachmentBytes=%d", eff.maxIter, eff.maxAttachmentBytes)
	}
}

// TestRunOptions_LimitsValidation verifies that negative values (except
// Unbounded on MaxSteps) are rejected by Validate.
func TestRunOptions_LimitsValidation(t *testing.T) {
	negIter := -1
	if err := (&RunOptions{Limits: &Limits{MaxIter: negIter}}).Validate(); err == nil {
		t.Fatalf("Limits.MaxIter=-1 should be rejected")
	}
	// Unbounded MaxSteps is a valid value, not a validation failure.
	if err := (&RunOptions{Limits: &Limits{MaxSteps: Unbounded}}).Validate(); err != nil {
		t.Fatalf("Limits.MaxSteps=Unbounded should be valid, got: %v", err)
	}
}
