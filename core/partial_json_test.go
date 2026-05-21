package core

import (
	"encoding/json"
	"testing"
)

func TestPartialJSONComplete(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"a":1}`))
	if !ok {
		t.Fatal("expected ok")
	}
	if string(got) != `{"a":1}` {
		t.Errorf("got %s", got)
	}
}

func TestPartialJSONOpenString(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"title":"hello wor`))
	if !ok {
		t.Fatal("expected ok")
	}
	// Closes the open string and the open object.
	var probe map[string]any
	if err := json.Unmarshal(got, &probe); err != nil {
		t.Fatalf("not valid JSON: %s (%v)", got, err)
	}
	if probe["title"] != "hello wor" {
		t.Errorf("title = %v", probe["title"])
	}
}

func TestPartialJSONOpenObject(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"a":1,`))
	if !ok {
		t.Fatal("expected ok")
	}
	// Trailing comma is dropped, object closed.
	if string(got) != `{"a":1}` {
		t.Errorf("got %s", got)
	}
}

func TestPartialJSONOpenArray(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"items":[1,2,`))
	if !ok {
		t.Fatal("expected ok")
	}
	if string(got) != `{"items":[1,2]}` {
		t.Errorf("got %s", got)
	}
}

func TestPartialJSONIncompleteNumber(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"n":12.`))
	if !ok {
		t.Fatal("expected ok")
	}
	// Incomplete number dropped along with the open key.
	if string(got) != `{}` {
		t.Errorf("got %s", got)
	}
}

func TestPartialJSONEscapedQuote(t *testing.T) {
	got, ok := PartialJSON([]byte(`{"a":"x\"y`))
	if !ok {
		t.Fatal("expected ok")
	}
	var probe map[string]any
	if err := json.Unmarshal(got, &probe); err != nil {
		t.Fatalf("not valid: %s (%v)", got, err)
	}
	if probe["a"] != `x"y` {
		t.Errorf("a = %v", probe["a"])
	}
}

func TestPartialJSONEmpty(t *testing.T) {
	_, ok := PartialJSON([]byte(``))
	if ok {
		t.Error("expected !ok for empty input")
	}
}

func TestPartialJSONPropertyAllPrefixesValid(t *testing.T) {
	// Inputs: well-formed JSON. Every byte prefix MUST yield either
	// (nil, false) or (snapshot, true) where snapshot is valid JSON.
	inputs := [][]byte{
		[]byte(`{"a":1,"b":[1,2,3]}`),
		[]byte(`["x","y","z"]`),
		[]byte(`{"nested":{"deep":{"k":"v"}}}`),
		[]byte(`{"s":"hello \"world\""}`),
		[]byte(`[true,false,null,42,3.14]`),
	}
	for _, full := range inputs {
		for i := 1; i <= len(full); i++ {
			prefix := full[:i]
			snap, ok := PartialJSON(prefix)
			if !ok {
				continue
			}
			if !json.Valid(snap) {
				t.Errorf("prefix %q produced invalid JSON: %s", prefix, snap)
			}
		}
	}
}
