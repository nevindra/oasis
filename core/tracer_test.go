package core

import "testing"

// TestSpanAttr_TypedAccessors verifies the typed accessors return the value
// with ok=true for the matching constructor and (zero, false) otherwise,
// without ever panicking. Val() still exposes the raw any for the OTEL bridge.
func TestSpanAttr_TypedAccessors(t *testing.T) {
	s := StringAttr("k", "v")
	if got, ok := s.Str(); !ok || got != "v" {
		t.Errorf("Str() = (%q, %v), want (\"v\", true)", got, ok)
	}
	if _, ok := s.Int(); ok {
		t.Error("Int() on a string attr should be (0, false)")
	}
	if _, ok := s.Float(); ok {
		t.Error("Float() on a string attr should be (0, false)")
	}
	if _, ok := s.Bool(); ok {
		t.Error("Bool() on a string attr should be (false, false)")
	}
	if s.Val() != "v" {
		t.Errorf("Val() = %v, want \"v\"", s.Val())
	}

	i := IntAttr("k", 42)
	if got, ok := i.Int(); !ok || got != 42 {
		t.Errorf("Int() = (%d, %v), want (42, true)", got, ok)
	}
	if _, ok := i.Str(); ok {
		t.Error("Str() on an int attr should be (\"\", false)")
	}

	f := Float64Attr("k", 1.5)
	if got, ok := f.Float(); !ok || got != 1.5 {
		t.Errorf("Float() = (%v, %v), want (1.5, true)", got, ok)
	}

	b := BoolAttr("k", true)
	if got, ok := b.Bool(); !ok || !got {
		t.Errorf("Bool() = (%v, %v), want (true, true)", got, ok)
	}
}
