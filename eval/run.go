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
	// Why: a failing scorer must not be silently treated as a 0.0 score in CI;
	// callers need to distinguish "scored 0" from "scorer errored". Nil on the
	// happy path — no allocation overhead when all scorers succeed.
	ScorerErrors map[string]error // keyed by scorer ID; nil when no scorer errors
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
		// Why: a bare blocking send on sem would stall here indefinitely when all
		// concurrency slots are busy, ignoring a cancelled context. The select
		// unblocks immediately on cancellation; wg.Done() balances the wg.Add(1)
		// above before we drain already-running goroutines with wg.Wait().
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done() // balance the wg.Add(1) for the goroutine we are not launching
			wg.Wait() // drain goroutines that are already running
			return EvalReport{}, ctx.Err()
		}
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
			// Why: silently continuing makes a misconfigured judge indistinguishable
			// from a 0.0 score in CI. Record the error keyed by scorer ID so callers
			// can detect and surface it. The map is lazily initialised for zero
			// allocation on the happy path.
			if res.ScorerErrors == nil {
				res.ScorerErrors = make(map[string]error)
			}
			res.ScorerErrors[sc.ID()] = serr
			continue
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
