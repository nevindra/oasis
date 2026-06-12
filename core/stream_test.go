package core

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFinishReasonValues(t *testing.T) {
	cases := []struct {
		got  FinishReason
		want string
	}{
		{FinishStop, "stop"},
		{FinishToolCalls, "tool-calls"},
		{FinishLength, "length"},
		{FinishContentFilter, "content-filter"},
		{FinishHalted, "halted"},
		{FinishSuspended, "suspended"},
		{FinishMaxIter, "max-iterations"},
		{FinishError, "error"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("FinishReason %q != %q", c.got, c.want)
		}
	}
}

func TestNewEventTypeValues(t *testing.T) {
	cases := []struct {
		got  StreamEventType
		want string
	}{
		{EventRunStart, "run-start"},
		{EventRunFinish, "run-finish"},
		{EventIterationStart, "iteration-start"},
		{EventIterationFinish, "iteration-finish"},
		{EventObjectDelta, "object-delta"},
		{EventObjectFinish, "object-finish"},
		{EventElementDelta, "element-delta"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("event type %q != %q", c.got, c.want)
		}
	}
}

// TestAllStreamEventTypes_Exhaustive parses stream.go with go/ast to collect
// every constant whose declared type is StreamEventType, then asserts that
// AllStreamEventTypes() returns exactly that set — no missing entries, no
// extras, no duplicates. The parser result is sanity-checked (> 20 constants)
// so a silently-broken parser cannot produce a vacuous pass.
func TestAllStreamEventTypes_Exhaustive(t *testing.T) {
	// --- Step 1: AST-derive the ground-truth set from stream.go ---
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	streamGoPath := filepath.Join(filepath.Dir(thisFile), "stream.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, streamGoPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("go/parser: %v", err)
	}

	// Walk all top-level const declarations and collect names whose type is
	// StreamEventType. Every spec in the block carries an explicit type in
	// stream.go, so we check ValueSpec.Type directly.
	fromAST := make(map[string]bool)
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		// Track the last seen type to handle inherited-type blocks (iota-style).
		var lastType string
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				if ident, ok := vs.Type.(*ast.Ident); ok {
					lastType = ident.Name
				} else {
					lastType = ""
				}
			}
			if lastType == "StreamEventType" {
				for _, name := range vs.Names {
					fromAST[name.Name] = true
				}
			}
		}
	}

	if len(fromAST) <= 20 {
		t.Fatalf("AST extraction looks broken: only found %d StreamEventType constants (expected > 20)", len(fromAST))
	}

	// --- Step 2: collect AllStreamEventTypes() result ---
	allSlice := AllStreamEventTypes()

	// Check for duplicates in the returned slice.
	seen := make(map[StreamEventType]int)
	for _, e := range allSlice {
		seen[e]++
	}
	for e, count := range seen {
		if count > 1 {
			t.Errorf("AllStreamEventTypes() contains duplicate entry %q (%d times)", e, count)
		}
	}

	// Build a string-keyed set from the slice. We map string values back to
	// constant names via the AST set for clear error messages.
	// Build reverse map: string value -> constant name(s) from AST.
	astValueToName := make(map[string]string)
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		var lastType string
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if vs.Type != nil {
				if ident, ok := vs.Type.(*ast.Ident); ok {
					lastType = ident.Name
				} else {
					lastType = ""
				}
			}
			if lastType != "StreamEventType" {
				continue
			}
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					if bl, ok := vs.Values[i].(*ast.BasicLit); ok {
						// Strip surrounding quotes from the string literal.
						val := bl.Value
						if len(val) >= 2 {
							val = val[1 : len(val)-1]
						}
						astValueToName[val] = name.Name
					}
				}
			}
		}
	}

	// Build set from slice (by string value).
	inSlice := make(map[string]bool, len(allSlice))
	for _, e := range allSlice {
		inSlice[string(e)] = true
	}

	// Direction 1: constants in AST that are missing from the slice.
	for name := range fromAST {
		// Find the string value for this name.
		found := false
		for val, n := range astValueToName {
			if n == name && inSlice[val] {
				found = true
				break
			}
		}
		if !found {
			// Find the value for better error message.
			var val string
			for v, n := range astValueToName {
				if n == name {
					val = v
					break
				}
			}
			t.Errorf("AllStreamEventTypes() is missing %s (%q)", name, val)
		}
	}

	// Direction 2: values in slice that have no corresponding AST constant.
	for val := range inSlice {
		if _, ok := astValueToName[val]; !ok {
			t.Errorf("AllStreamEventTypes() contains %q which has no StreamEventType constant in stream.go", val)
		}
	}

	if !t.Failed() {
		t.Logf("OK: AST found %d StreamEventType constants, slice has %d entries, all match", len(fromAST), len(allSlice))
	}
}

func TestStreamEventNewFieldsRoundTrip(t *testing.T) {
	ev := StreamEvent{
		Type:         EventRunFinish,
		FinishReason: FinishStop,
		Warnings:     []string{"rate-limited"},
		ProviderMeta: []byte(`{"stop_sequence":"END"}`),
		Object:       []byte(`{"title":"x"}`),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back StreamEvent
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.FinishReason != FinishStop {
		t.Errorf("FinishReason lost: %q", back.FinishReason)
	}
	if len(back.Warnings) != 1 || back.Warnings[0] != "rate-limited" {
		t.Errorf("Warnings lost: %#v", back.Warnings)
	}
	if string(back.ProviderMeta) != `{"stop_sequence":"END"}` {
		t.Errorf("ProviderMeta lost: %s", back.ProviderMeta)
	}
	if string(back.Object) != `{"title":"x"}` {
		t.Errorf("Object lost: %s", back.Object)
	}
}
