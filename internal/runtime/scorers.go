package runtime

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/oasis/core"
)

const (
	defaultScorerWorkers = 4
	defaultScorerBuffer  = 256
)

// HasScorers reports whether any scorers are attached to this runtime.
func (c *Runtime) HasScorers() bool { return len(c.Scorers) > 0 }

// scorerPool runs async (LLM-judge) scorers off the hot path. Bounded buffer +
// fixed workers: a full buffer DROPS the job (hard memory ceiling) rather than
// blocking the run or growing unbounded. Workers drain on close().
type scorerPool struct {
	jobs    chan scoreJob
	wg      sync.WaitGroup
	store   core.ScoreStore
	sink    core.ScoreSink
	logger  *slog.Logger
	dropped atomic.Int64
}

type scoreJob struct {
	scorer     core.Scorer
	run        core.ScorerRun
	entityID   string
	entityType string
}

func newScorerPool(workers, buffer int, store core.ScoreStore, sink core.ScoreSink, logger *slog.Logger) *scorerPool {
	if logger == nil {
		logger = slog.Default()
	}
	p := &scorerPool{
		jobs:   make(chan scoreJob, buffer),
		store:  store,
		sink:   sink,
		logger: logger,
	}
	for range workers {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *scorerPool) worker() {
	defer p.wg.Done()
	for job := range p.jobs {
		p.process(job)
	}
}

func (p *scorerPool) process(job scoreJob) {
	// Async work outlives the originating request; use a fresh background
	// context so a cancelled request does not abort persistence.
	ctx := context.Background()
	sc, err := job.scorer.Score(ctx, job.run)
	if err != nil {
		p.logger.Warn("eval: async scorer failed", "scorer", job.scorer.ID(), "error", err)
		return
	}
	row := newScoreRow(sc, job.run, job.entityID, job.entityType)
	if p.store != nil {
		if err := p.store.SaveScores(ctx, []core.ScoreRow{row}); err != nil {
			p.logger.Warn("eval: save score failed", "scorer", sc.ScorerID, "error", err)
		}
	}
	if p.sink != nil {
		if err := p.sink.Emit(ctx, row); err != nil {
			p.logger.Warn("eval: sink emit failed", "scorer", sc.ScorerID, "error", err)
		}
	}
}

// submit enqueues a job without blocking. A full buffer drops the job (memory
// ceiling) and increments the dropped counter.
func (p *scorerPool) submit(job scoreJob) {
	select {
	case p.jobs <- job:
	default:
		p.dropped.Add(1)
		p.logger.Warn("eval: async score dropped, queue full", "scorer", job.scorer.ID())
	}
}

// close stops accepting work and drains in-flight jobs. Nil-safe.
func (p *scorerPool) close() {
	if p == nil {
		return
	}
	close(p.jobs)
	p.wg.Wait()
}

func newScoreRow(sc core.Score, run core.ScorerRun, entityID, entityType string) core.ScoreRow {
	return core.ScoreRow{
		ID:         core.NewID(),
		ScorerID:   sc.ScorerID,
		RunID:      run.RunID,
		EntityID:   entityID,
		EntityType: entityType,
		Input:      run.Input,
		Output:     run.Output,
		Value:      sc.Value,
		Reason:     sc.Reason,
		Details:    sc.Details,
		Source:     run.Source,
		CreatedAt:  time.Now(),
	}
}

// RunScorers is the exported entry point for agent/llm.go (which cannot call
// the unexported runScorers across the package boundary even via embedding).
func (c *Runtime) RunScorers(ctx context.Context, input string, res core.AgentResult) core.AgentResult {
	return c.runScorers(ctx, input, res)
}

// runScorers runs all configured scorers against a completed run. Inline scorers
// run synchronously and their results append to the returned AgentResult.Scores;
// async scorers submit to the pool. Returns res unchanged when no scorers are
// configured (the common-case fast path — zero allocation).
func (c *Runtime) runScorers(ctx context.Context, input string, res core.AgentResult) core.AgentResult {
	if len(c.Scorers) == 0 {
		return res
	}
	run := core.ScorerRun{
		Input:      input,
		Output:     res.Output,
		Thinking:   res.Thinking,
		Iterations: res.Iterations,
		Steps:      res.Steps,
		Source:     core.ScorerSourceLive,
	}
	for _, sc := range c.Scorers {
		if !sampleHit(sc.Sampling.Rate) {
			continue
		}
		if scorerIsAsync(sc) {
			if c.scorePool != nil {
				c.scorePool.submit(scoreJob{scorer: sc.Scorer, run: run, entityID: c.name, entityType: "agent"})
			}
			continue
		}
		s, err := sc.Scorer.Score(ctx, run)
		if err != nil {
			// Use c.Config.Logger, not c.Logger: Runtime has a Logger() method
			// that shadows the promoted Config.Logger field.
			if c.Config.Logger != nil {
				c.Config.Logger.Warn("eval: inline scorer failed", "scorer", sc.Scorer.ID(), "error", err)
			}
			continue
		}
		res.Scores = append(res.Scores, s)
	}
	return res
}

// sampleHit reports whether a run is sampled. Rate <= 0 or >= 1 means always;
// otherwise probabilistic.
func sampleHit(rate float64) bool {
	if rate <= 0 || rate >= 1 {
		return true
	}
	return rand.Float64() < rate
}

// scorerIsAsync resolves the execution mode for a scorer config. Auto consults
// the optional core.AsyncScorer capability.
func scorerIsAsync(sc core.ScorerConfig) bool {
	switch sc.Mode {
	case core.ScoreModeInline:
		return false
	case core.ScoreModeAsync:
		return true
	default: // ScoreModeAuto
		as, ok := sc.Scorer.(core.AsyncScorer)
		return ok && as.PrefersAsync()
	}
}
