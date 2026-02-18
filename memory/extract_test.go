package memory

import "testing"

func TestShouldExtractTrivial(t *testing.T) {
	for _, s := range []string{"ok", "Oke", "thanks", "sip", "lol", "wkwk", "ya", "short"} {
		if ShouldExtract(s) {
			t.Errorf("should skip: %q", s)
		}
	}
}

func TestShouldExtractReal(t *testing.T) {
	for _, s := range []string{
		"Gue tinggal di Jakarta sekarang",
		"I work as a software engineer",
		"My name is Nev and I like Rust",
	} {
		if !ShouldExtract(s) {
			t.Errorf("should extract: %q", s)
		}
	}
}

func TestParseFactsBasic(t *testing.T) {
	r := `[{"fact":"User's name is Nev","category":"personal"},{"fact":"Works as a software engineer","category":"work"}]`
	facts := ParseExtractedFacts(r)
	if len(facts) != 2 {
		t.Fatalf("expected 2, got %d", len(facts))
	}
	if facts[0].Fact != "User's name is Nev" {
		t.Error("wrong fact")
	}
	if facts[1].Category != "work" {
		t.Error("wrong category")
	}
}

func TestParseFactsEmpty(t *testing.T) {
	facts := ParseExtractedFacts("[]")
	if len(facts) != 0 {
		t.Error("expected empty")
	}
}

func TestParseFactsInvalidJSON(t *testing.T) {
	facts := ParseExtractedFacts("This is not JSON")
	if facts != nil {
		t.Error("expected nil")
	}
}

func TestParseFactsMarkdownFence(t *testing.T) {
	r := "```json\n[{\"fact\":\"User likes Go\",\"category\":\"preference\"}]\n```"
	facts := ParseExtractedFacts(r)
	if len(facts) != 1 {
		t.Fatalf("expected 1, got %d", len(facts))
	}
	if facts[0].Fact != "User likes Go" {
		t.Errorf("fact = %q, want %q", facts[0].Fact, "User likes Go")
	}
}

func TestParseFactsWithSupersedes(t *testing.T) {
	r := `[{"fact":"User moved to Bali","category":"personal","supersedes":"Lives in Jakarta"}]`
	facts := ParseExtractedFacts(r)
	if len(facts) != 1 {
		t.Fatal("expected 1")
	}
	if facts[0].Supersedes == nil || *facts[0].Supersedes != "Lives in Jakarta" {
		t.Error("wrong supersedes")
	}
}

func TestParseFactsWithoutSupersedes(t *testing.T) {
	r := `[{"fact":"User's name is Nev","category":"personal"}]`
	facts := ParseExtractedFacts(r)
	if len(facts) != 1 {
		t.Fatal("expected 1")
	}
	if facts[0].Supersedes != nil {
		t.Error("should be nil")
	}
}
