// memory/options_test.go
package memory

import (
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestOptions_Apply(t *testing.T) {
	cfg := AgentMemoryConfig{}
	store := newConformanceStore(t)
	WithStore(store)(&cfg)
	WithHistory(HistoryConfig{MaxMessages: 20})(&cfg)
	WithSemanticRecall()(&cfg)
	WithRecallKinds(KindFact, KindEvent)(&cfg)
	WithAutoTitle()(&cfg)

	if cfg.Store != store {
		t.Fatal("Store not set")
	}
	if cfg.MaxHistory != 20 {
		t.Fatal("MaxHistory not set")
	}
	if !cfg.SemanticRecall {
		t.Fatal("SemanticRecall not set")
	}
	if len(cfg.RecallKinds) != 2 {
		t.Fatal("RecallKinds not set")
	}
	if !cfg.AutoTitle {
		t.Fatal("AutoTitle not set")
	}
	_ = core.NowUnix
}
