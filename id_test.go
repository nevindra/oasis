package oasis

import "testing"

func TestNewID(t *testing.T) {
	id1 := NewID()
	id2 := NewID()
	if len(id1) != 20 {
		t.Errorf("expected 20 chars (xid), got %d: %s", len(id1), id1)
	}
	if id1 == id2 {
		t.Error("two IDs should be unique")
	}
}
