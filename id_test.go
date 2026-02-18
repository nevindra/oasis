package oasis

import "testing"

func TestNewID(t *testing.T) {
	id1 := NewID()
	id2 := NewID()
	if len(id1) != 36 {
		t.Errorf("expected 36 chars (UUIDv7), got %d: %s", len(id1), id1)
	}
	if id1 == id2 {
		t.Error("two IDs should be unique")
	}
	if id1 >= id2 {
		t.Error("sequential UUIDv7s should be time-ordered")
	}
}
