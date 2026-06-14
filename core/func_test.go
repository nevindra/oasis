package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type addInput struct {
	A int `json:"a" describe:"first number"`
	B int `json:"b" describe:"second number"`
}

func TestFunc_BasicRoundTrip(t *testing.T) {
	tool := Func("add", "Add two numbers",
		func(_ context.Context, in addInput) (int, error) {
			return in.A + in.B, nil
		})

	res, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"a":3,"b":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Content) != "7" {
		t.Errorf("got %s, want 7", res.Content)
	}
}

type lookupResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestFunc_StructOutput(t *testing.T) {
	tool := Func("lookup", "Look up a user",
		func(_ context.Context, in struct {
			ID string `json:"id"`
		}) (lookupResult, error) {
			return lookupResult{ID: in.ID, Name: "alice"}, nil
		})

	res, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"id":"u1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out lookupResult
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "u1" || out.Name != "alice" {
		t.Errorf("got %+v", out)
	}
}

func TestFunc_StringOutput(t *testing.T) {
	tool := Func("greet", "Greet a user",
		func(_ context.Context, in struct {
			Name string `json:"name"`
		}) (string, error) {
			return "hello " + in.Name, nil
		})

	res, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"name":"bob"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Func marshals the string output to JSON, so Content = `"hello bob"` (with quotes).
	// Text() returns Content as-is.
	if res.Text() != `"hello bob"` {
		t.Errorf("got %q, want %q", res.Text(), `"hello bob"`)
	}
}

func TestFunc_SchemaMatchesDeriveSchema(t *testing.T) {
	tool := Func("add", "Add", func(_ context.Context, in addInput) (int, error) { return 0, nil })
	derived := DeriveSchema[addInput]()
	got := tool.Definition().Parameters
	if string(got) != string(derived) {
		t.Errorf("schema mismatch:\n  Func:   %s\n  Derive: %s", got, derived)
	}
}

func TestFunc_OutputSchemaMatchesErase(t *testing.T) {
	// Func must derive the same OutputSchema as Erase for the same Out type,
	// so the published tool shape is identical regardless of the authoring path.
	fnTool := Func("lookup", "Look up a user",
		func(_ context.Context, in struct {
			ID string `json:"id"`
		}) (lookupResult, error) {
			return lookupResult{ID: in.ID}, nil
		})

	erased := Erase[struct {
		ID string `json:"id"`
	}, lookupResult](structOutTool{})

	got := fnTool.Definition().OutputSchema
	want := erased.Definition().OutputSchema
	if len(got) == 0 {
		t.Fatal("expected non-empty Func OutputSchema")
	}
	if string(got) != string(want) {
		t.Errorf("OutputSchema mismatch:\n  Func:  %s\n  Erase: %s", got, want)
	}
	// Also pin it directly to DeriveSchema[Out] (the documented derivation).
	if derived := DeriveSchema[lookupResult](); string(got) != string(derived) {
		t.Errorf("OutputSchema != DeriveSchema[Out]:\n  Func:   %s\n  Derive: %s", got, derived)
	}
}

// structOutTool is a minimal Tool[In, Out] used to compare Func's derived
// OutputSchema against Erase's for the same Out type.
type structOutTool struct{}

func (structOutTool) Definition() ToolMeta {
	return ToolMeta{Name: "lookup", Description: "Look up a user"}
}
func (structOutTool) Execute(_ context.Context, _ struct {
	ID string `json:"id"`
}) (lookupResult, error) {
	return lookupResult{}, nil
}

func TestFunc_NameAndDefinition(t *testing.T) {
	tool := Func("mytool", "My description",
		func(_ context.Context, _ struct{}) (string, error) { return "", nil })

	if tool.Name() != "mytool" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "mytool")
	}
	def := tool.Definition()
	if def.Name != "mytool" {
		t.Errorf("Definition().Name = %q", def.Name)
	}
	if def.Description != "My description" {
		t.Errorf("Definition().Description = %q", def.Description)
	}
}

func TestFunc_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("tool failed")
	tool := Func("fail", "Always fails",
		func(_ context.Context, _ struct{}) (string, error) { return "", sentinel })

	res, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Errorf("non-InfraError must not propagate Go error, got %v", err)
	}
	if res.Error != "tool failed" {
		t.Errorf("ToolResult.Error = %q", res.Error)
	}
}

func TestFunc_InfraErrorPropagation(t *testing.T) {
	sentinel := errors.New("network down")
	tool := Func("infra-fail", "Fails with infra error",
		func(_ context.Context, _ struct{}) (string, error) {
			return "", InfraError(sentinel)
		})

	res, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("InfraError must propagate Go error, got nil")
	}
	if !IsInfraError(err) {
		t.Errorf("Go error is not InfraError: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("Go error does not unwrap to sentinel: %v", err)
	}
	if res.Error != "network down" {
		t.Errorf("ToolResult.Error = %q, want %q", res.Error, "network down")
	}
}

func TestFunc_InvalidArgs(t *testing.T) {
	tool := Func("add", "Add", func(_ context.Context, in addInput) (int, error) { return 0, nil })

	res, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`not json`))
	if err != nil {
		t.Errorf("expected nil Go error for invalid args, got %v", err)
	}
	if res.Error == "" {
		t.Error("expected ToolResult.Error to be set for invalid args")
	}
}

func TestFunc_PanicOnUnsupportedType(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unsupported input type")
		}
	}()
	type bad struct {
		Ch chan int `json:"ch"`
	}
	Func("bad", "bad", func(_ context.Context, _ bad) (string, error) { return "", nil })
}

// Compile-time check: funcTool satisfies AnyTool.
var _ AnyTool = Func("x", "x", func(context.Context, struct{}) (string, error) { return "", nil })
