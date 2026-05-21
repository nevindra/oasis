package core

import (
	"encoding/json"
	"testing"
)

func TestSourceRoundTrip(t *testing.T) {
	src := Source{
		URL:    "https://example.com/doc",
		Title:  "Example",
		Quote:  "the relevant passage",
		Origin: "rag",
		Meta:   []byte(`{"score":0.87}`),
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Source
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.URL != src.URL || back.Origin != src.Origin {
		t.Errorf("Source lost: %+v", back)
	}
}

// fakeSourced is a minimal implementation used to verify the interface is satisfiable.
type fakeSourced struct{ srcs []Source }

func (f fakeSourced) Sources() []Source { return f.srcs }

func TestSourcedInterface(t *testing.T) {
	var s Sourced = fakeSourced{srcs: []Source{{URL: "x"}}}
	if len(s.Sources()) != 1 {
		t.Errorf("Sourced interface broken")
	}
}

type fakeWarner struct{ ws []string }

func (f fakeWarner) Warnings() []string { return f.ws }

func TestWarnerInterface(t *testing.T) {
	var w Warner = fakeWarner{ws: []string{"hi"}}
	if len(w.Warnings()) != 1 {
		t.Errorf("Warner interface broken")
	}
}
