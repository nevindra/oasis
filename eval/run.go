package eval

import (
	"context"
	"sort"
	"sync"

	"github.com/nevindra/oasis/core"
)

// EvalItem is one test case for RunEvals.
type EvalItem struct {
	Input       string
	GroundTruth string
	Context     []string
	Expected    *core.ExpectedTrajectory
}

// EvalResult is the outcome of running and scoring one EvalItem.
type EvalResult struct {
	Item   EvalItem
	Result core.AgentResult
	Scores []core.Score
	Err    error // non-nil if the agent run failed (scorers were skipped)
}

// RunEvalsConfig configures a batch evaluation.
type RunEvalsConfig struct {
	Agent       core.Agent
	Data        []EvalItem
	Scorers     []core.Scorer
	Concurrency int              // <= 0 → 4
	OnItem      func(EvalResult) // optional; called once per item (serialized)
}

// EvalReport holds per-scorer aggregate statistics across all items, keyed by
// scorer ID. Use it for CI gates: if rep.Mean["faithfulness"] < 0.8 { fail }.
type EvalReport struct {
	N      int
	Failed int
	Mean   map[string]float64
	Min    map[string]float64
	Max    map[string]float64
	P50    map[string]float64
	P95    map[string]float64
}

// RunEvals runs cfg.Agent against every item with bounded concurrency, scores
// each successful run with all scorers (Source = ScorerSourceTest), invokes
// OnItem per item, and returns aggregate statistics. Agent run failures are
// recorded in EvalResult.Err and counted in EvalReport.Failed — they do not
// abort the batch. RunEvals returns a non-nil error only if ctx is cancelled.
func RunEvals(ctx context.Context, cfg RunEvalsConfig) (EvalReport, error) {
	conc := cfg.Concurrency
	if conc <= 0 {
		conc = 4
	}
	results := make([]EvalResult, len(cfg.Data))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex // serializes the user OnItem callback

	for i := range cfg.Data {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = evalOne(ctx, cfg, cfg.Data[i])
			if cfg.OnItem != nil {
				mu.Lock()
				cfg.OnItem(results[i])
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return EvalReport{}, err
	}
	return aggregate(results), nil
}

func evalOne(ctx context.Context, cfg RunEvalsConfig, item EvalItem) EvalResult {
	res := EvalResult{Item: item}
	out, err := cfg.Agent.Execute(ctx, core.AgentTask{Input: item.Input})
	if err != nil {
		res.Err = err
		return res
	}
	res.Result = out
	run := core.ScorerRun{
		Input:       item.Input,
		Output:      out.Output,
		Thinking:    out.Thinking,
		GroundTruth: item.GroundTruth,
		Context:     item.Context,
		Iterations:  out.Iterations,
		Steps:       out.Steps,
		Expected:    item.Expected,
		Source:      core.ScorerSourceTest,
	}
	for _, sc := range cfg.Scorers {
		s, serr := sc.Score(ctx, run)
		if serr != nil {
			continue // a failing scorer is recorded as absent, not fatal
		}
		res.Scores = append(res.Scores, s)
	}
	return res
}

func aggregate(results []EvalResult) EvalReport {
	rep := EvalReport{
		Mean: map[string]float64{},
		Min:  map[string]float64{},
		Max:  map[string]float64{},
		P50:  map[string]float64{},
		P95:  map[string]float64{},
	}
	byScorer := map[string][]float64{}
	for _, r := range results {
		rep.N++
		if r.Err != nil {
			rep.Failed++
		}
		for _, s := range r.Scores {
			byScorer[s.ScorerID] = append(byScorer[s.ScorerID], s.Value)
		}
	}
	for id, vals := range byScorer {
		sort.Float64s(vals)
		rep.Min[id] = vals[0]
		rep.Max[id] = vals[len(vals)-1]
		rep.Mean[id] = mean(vals)
		rep.P50[id] = percentile(vals, 0.50)
		rep.P95[id] = percentile(vals, 0.95)
	}
	return rep
}

func mean(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// percentile returns the value at the given quantile of a pre-sorted slice
// using nearest-rank rounding.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p*float64(len(sorted)-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
