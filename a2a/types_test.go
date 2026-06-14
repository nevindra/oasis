package a2a

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoldenRoundTrip unmarshals every official spec fixture into our types
// and re-marshals, requiring semantic equality. Catches drift from the
// standard, not just from ourselves.
func TestGoldenRoundTrip(t *testing.T) {
	files, err := filepath.Glob("testdata/golden/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden fixtures: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var target any
			base := filepath.Base(f)
			switch {
			case strings.HasPrefix(base, "agent-card"):
				target = &AgentCard{}
			case strings.HasPrefix(base, "task"):
				target = &Task{}
			case strings.HasPrefix(base, "message"):
				target = &Message{}
			default:
				target = &StreamResponse{}
			}
			if err := json.Unmarshal(raw, target); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			out, err := json.Marshal(target)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var want, got any
			json.Unmarshal(raw, &want)
			json.Unmarshal(out, &got)
			if !jsonEqual(want, got) {
				t.Errorf("round-trip mismatch:\nwant %s\ngot  %s", raw, out)
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// TestSendConfigurationBlockingDefault pins the zero-value semantics: a
// zero-value SendConfiguration (and a nil/true Blocking) means blocking — the
// documented A2A default — while only an explicit *false opts out. This guards
// the freeze-shape trap where a plain `bool` would have defaulted to
// non-blocking.
func TestSendConfigurationBlockingDefault(t *testing.T) {
	cases := []struct {
		name           string
		cfg            *SendConfiguration
		wantNonBlock   bool
		wantWireHasKey bool // whether marshaled JSON carries the "blocking" key
	}{
		{"nil config", nil, false, false},
		{"zero value", &SendConfiguration{}, false, false},
		{"explicit blocking", &SendConfiguration{Blocking: BlockingPtr()}, false, true},
		{"explicit non-blocking", &SendConfiguration{Blocking: NonBlockingPtr()}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.isNonBlocking(); got != tc.wantNonBlock {
				t.Errorf("isNonBlocking() = %v, want %v", got, tc.wantNonBlock)
			}
			if tc.cfg == nil {
				return
			}
			raw, err := json.Marshal(tc.cfg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Wire key must stay "blocking" (A2A v1.0 SendMessageConfiguration),
			// and must be omitted entirely when Blocking is nil so a default
			// config round-trips as a blocking one.
			hasKey := strings.Contains(string(raw), `"blocking"`)
			if hasKey != tc.wantWireHasKey {
				t.Errorf("wire = %s; blocking-key present = %v, want %v", raw, hasKey, tc.wantWireHasKey)
			}
			if strings.Contains(string(raw), "nonBlocking") {
				t.Errorf("wire must use the spec key \"blocking\", not \"nonBlocking\": %s", raw)
			}

			// A config decoded from JSON with no "blocking" key must come back
			// blocking (Blocking == nil).
			var rt SendConfiguration
			if err := json.Unmarshal(raw, &rt); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if rt.isNonBlocking() != tc.wantNonBlock {
				t.Errorf("round-trip isNonBlocking() = %v, want %v", rt.isNonBlocking(), tc.wantNonBlock)
			}
		})
	}
}

func TestTaskStateTerminal(t *testing.T) {
	for _, tc := range []struct {
		s    TaskState
		want bool
	}{
		{TaskStateCompleted, true},
		{TaskStateFailed, true},
		{TaskStateCanceled, true},
		{TaskStateRejected, true},
		{TaskStateWorking, false},
		{TaskStateInputRequired, false},
		{TaskStateSubmitted, false},
	} {
		if got := tc.s.Terminal(); got != tc.want {
			t.Errorf("%s.Terminal() = %v, want %v", tc.s, got, tc.want)
		}
	}
}
