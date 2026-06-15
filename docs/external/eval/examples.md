# eval examples

Runnable recipes. All scorers below are user-defined — no built-in scorers ship yet. Import `core` and `agent` directly; these types are not re-exported at the root `oasis` package.

---

## Minimal deterministic scorer (inline)

A keyword-coverage scorer checks whether a required set of keywords appears in the agent output. It is cheap enough to run inline.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "strings"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/provider/gemini"
)

// KeywordCoverageScorer checks that the output contains all required keywords.
type KeywordCoverageScorer struct {
    keywords []string
}

func (s *KeywordCoverageScorer) ID() string { return "keyword-coverage-v1" }

func (s *KeywordCoverageScorer) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
    out := strings.ToLower(run.Output)
    hit := 0
    for _, kw := range s.keywords {
        if strings.Contains(out, strings.ToLower(kw)) {
            hit++
        }
    }

    value := float64(hit) / float64(len(s.keywords))
    reason := fmt.Sprintf("%d/%d keywords found", hit, len(s.keywords))

    return core.Score{
        ScorerID: s.ID(),
        Value:    value,
        Reason:   reason,
    }, nil
}

func main() {
    ctx := context.Background()

    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")

    scorer := &KeywordCoverageScorer{
        keywords: []string{"transformer", "attention", "encoder", "decoder"},
    }

    a := agent.New("assistant", "Helpful assistant", llm,
        agent.WithScorers(core.ScorerConfig{
            Scorer: scorer,
            Mode:   core.ScoreModeInline, // explicit; also the default for plain Scorer
        }),
    )

    result, err := a.Execute(ctx, agent.AgentTask{
        Input: "Explain how a transformer model works in two sentences.",
    })
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println("Output:", result.Output)

    // Inline scores are on AgentResult.Scores before Execute returns.
    for _, sc := range result.Scores {
        fmt.Printf("Scorer %s: %.2f — %s\n", sc.ScorerID, sc.Value, sc.Reason)
    }
}
```

Key points:
- `Score` returns an error only for infrastructure failures. A zero-keyword match is `Value: 0.0`, not an error.
- Inline scores appear in `result.Scores` immediately after `Execute` returns.
- No `ScoreStore` is needed for this recipe — scores exist only in memory.

---

## Async LLM-judge scorer with SQLite persistence

An LLM-judge scorer makes an outbound LLM call to rate quality. That extra latency must not block the caller, so the scorer implements `core.AsyncScorer`. Scores are persisted to SQLite and queried after the agent closes.

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "strings"
    "time"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/provider/gemini"
    "github.com/nevindra/oasis/store/sqlite"
)

// RelevanceJudge uses an LLM to score output relevance relative to the input.
// It implements core.AsyncScorer so the framework runs it off the hot path.
type RelevanceJudge struct {
    judgeProvider core.Provider // a separate, low-cost LLM for judging
}

func (j *RelevanceJudge) ID() string         { return "relevance-judge-v1" }
func (j *RelevanceJudge) PrefersAsync() bool { return true }

func (j *RelevanceJudge) Score(ctx context.Context, run core.ScorerRun) (core.Score, error) {
    // Build a simple 0-10 prompt and parse the numeric response.
    prompt := fmt.Sprintf(
        "Rate how relevant this answer is to the question on a scale of 0–10.\n"+
            "Question: %s\nAnswer: %s\n"+
            "Reply with only the number.",
        run.Input, run.Output,
    )

    resp, err := core.Chat(ctx, j.judgeProvider, core.ChatRequest{
        Messages: []core.ChatMessage{core.UserMessage(prompt)},
    })
    if err != nil {
        return core.Score{}, fmt.Errorf("judge LLM call: %w", err)
    }

    var rating float64
    if _, err := fmt.Sscanf(strings.TrimSpace(resp.Content), "%f", &rating); err != nil {
        return core.Score{}, fmt.Errorf("parse judge response %q: %w", resp.Content, err)
    }

    value := rating / 10.0
    details, _ := json.Marshal(map[string]any{"raw_rating": rating})

    return core.Score{
        ScorerID: j.ID(),
        Value:    value,
        Reason:   fmt.Sprintf("LLM rated output %g/10", rating),
        Details:  details,
    }, nil
}

func main() {
    ctx := context.Background()

    // One provider for the agent, one for the judge (can be the same or different).
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")
    judgeProvider := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash-lite")

    // Construct the store and run migrations (creates the scores table too).
    s := sqlite.New("data/eval.db")
    if err := s.Init(ctx); err != nil {
        log.Fatal(err)
    }
    defer s.Close()

    judge := &RelevanceJudge{judgeProvider: judgeProvider}

    a := agent.New("assistant", "Helpful assistant", llm,
        agent.WithScorers(core.ScorerConfig{
            Scorer: judge,
            Mode:   core.ScoreModeAuto, // async because judge.PrefersAsync() == true
        }),
        // WithScoreStore is explicit — not auto-detected from a memory store.
        agent.WithScoreStore(s),
    )
    defer a.Close() // drains the async worker pool before returning

    result, err := a.Execute(ctx, agent.AgentTask{
        Input: "What is the capital of France?",
    })
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println("Output:", result.Output)
    // Async scores are NOT in result.Scores — they are still in flight.
    fmt.Printf("Inline scores: %d (async scores pending)\n", len(result.Scores))

    // Close drains the pool; after this all async scores have been persisted.
    a.Close()

    // Query the store.
    rows, err := s.ListScores(ctx, core.ScoreFilter{
        ScorerID: judge.ID(),
        Limit:    10,
    })
    if err != nil {
        log.Fatal(err)
    }

    for _, row := range rows {
        fmt.Printf("[%s] scorer=%s value=%.2f reason=%q\n",
            row.CreatedAt.Format(time.RFC3339),
            row.ScorerID,
            row.Value,
            row.Reason,
        )
    }
}
```

Key points:
- `PrefersAsync()` returning `true` tells the framework to run this scorer off the hot path under `ScoreModeAuto`.
- `agent.WithScoreStore(s)` must be explicit — `s` is not auto-discovered even if it was also passed to `WithStore`.
- `a.Close()` drains the worker pool. Call it (or defer it) before querying the store, or async scores may not have been written yet.
- Async scores do not appear in `result.Scores`; read them from the store after `Close`.

---

## Per-scorer sampling

Sample your LLM-judge scorer at 20 % on live traffic to reduce cost, while running a cheap deterministic scorer on every run.

```go
package main

import (
    "context"
    "os"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    ctx := context.Background()
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")

    deterministicScorer := &KeywordCoverageScorer{
        keywords: []string{"transformer", "attention"},
    }
    expensiveJudge := &RelevanceJudge{ /* ... */ }

    a := agent.New("assistant", "Helpful assistant", llm,
        agent.WithScorers(
            // Runs on every live run (Rate <= 0 → always-on).
            core.ScorerConfig{
                Scorer:   deterministicScorer,
                Mode:     core.ScoreModeInline,
                Sampling: core.Sampling{Rate: 0}, // always-on
            },
            // Runs on roughly 20 % of live runs; async to keep hot path fast.
            core.ScorerConfig{
                Scorer:   expensiveJudge,
                Mode:     core.ScoreModeAuto,
                Sampling: core.Sampling{Rate: 0.2},
            },
        ),
    )

    // Every agent.Execute run is scored as LIVE (Source = ScorerSourceLive),
    // so per-scorer sampling applies automatically. AgentTask has no Source field.
    result, err := a.Execute(ctx, agent.AgentTask{
        Input: "Explain attention mechanisms.",
    })
    _ = result
    _ = err
    _ = ctx
}
```

Key points:
- `Rate: 0` (or any non-positive value) means always-on — the scorer fires on every live run.
- `Rate: 0.2` fires on approximately 20 % of live runs. The check happens before any scorer work, so sampled-out runs pay zero evaluation cost.
- Test runs (`ScorerSourceTest`) always score regardless of `Rate`.

---

## Reading scores from the store (analysis query)

After a period of production traffic, pull scores for a specific scorer to build a quality trend.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/store/sqlite"
)

func main() {
    ctx := context.Background()

    s := sqlite.New("data/eval.db")
    if err := s.Init(ctx); err != nil {
        log.Fatal(err)
    }
    defer s.Close()

    // All live scores from the relevance judge in the last 7 days.
    rows, err := s.ListScores(ctx, core.ScoreFilter{
        ScorerID: "relevance-judge-v1",
        Source:   core.ScorerSourceLive,
        Since:    time.Now().AddDate(0, 0, -7),
        Limit:    500,
    })
    if err != nil {
        log.Fatal(err)
    }

    var total float64
    for _, row := range rows {
        total += row.Value
    }

    if len(rows) > 0 {
        fmt.Printf("Mean relevance (last 7 days, %d runs): %.3f\n",
            len(rows), total/float64(len(rows)))
    } else {
        fmt.Println("No scores found.")
    }
}
```

---

## Using built-in scorers

Attach a deterministic built-in scorer inline and an LLM-judge scorer async in a single `agent.New` call. The keyword-coverage scorer runs inline and its result appears in `result.Scores`; the faithfulness judge runs async and its result goes to the `ScoreStore` only.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/eval"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    ctx := context.Background()

    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")

    // A separate, lower-cost provider for the LLM judge.
    judgeProvider := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash-lite")

    a := agent.New("assistant", "Helpful assistant", llm,
        agent.WithScorers(
            // Deterministic — inline by default; result lands on AgentResult.Scores.
            core.ScorerConfig{
                Scorer: eval.KeywordCoverage("transformer", "attention"),
                Mode:   core.ScoreModeInline,
            },
            // LLM judge — implements AsyncScorer; runs off the hot path under ScoreModeAuto.
            core.ScorerConfig{
                Scorer: eval.Faithfulness(judgeProvider),
                Mode:   core.ScoreModeAuto,
            },
        ),
    )

    result, err := a.Execute(ctx, agent.AgentTask{
        Input: "Explain how a transformer model works in two sentences.",
    })
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println("Output:", result.Output)

    // Only inline scores (keyword_coverage) appear here.
    // The async faithfulness judge result goes to the ScoreStore after the pool drains.
    for _, sc := range result.Scores {
        fmt.Printf("Scorer %s: %.2f — %s\n", sc.ScorerID, sc.Value, sc.Reason)
    }
}
```

Key points:
- Built-in scorers are constructed from `github.com/nevindra/oasis/eval` — no boilerplate scorer struct needed.
- `eval.KeywordCoverage` is a deterministic scorer; it runs inline and its score is available immediately in `result.Scores`.
- `eval.Faithfulness` implements `core.AsyncScorer`, so under `ScoreModeAuto` it is enqueued into the background worker pool and does not block `Execute`.

---

## Offline batch eval with RunEvals

Build a dataset of `eval.EvalItem` records, run them against an agent, and assert on the aggregate report for CI gating.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/eval"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    ctx := context.Background()

    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")

    a := agent.New("assistant", "Helpful assistant", llm)

    cases := []eval.EvalItem{
        {
            Input:       "What is the capital of France?",
            GroundTruth: "Paris",
        },
        {
            Input:       "What is the capital of Germany?",
            GroundTruth: "Berlin",
        },
        {
            Input:       "What is the capital of Japan?",
            GroundTruth: "Tokyo",
        },
    }

    rep, err := eval.RunEvals(ctx, eval.RunEvalsConfig{
        Agent: a,
        Data:  cases,
        Scorers: []core.Scorer{
            eval.ExactMatch(),
            eval.ContentSimilarity(),
        },
        Concurrency: 2,
    })
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Evaluated %d items, %d failed\n", rep.N, rep.Failed)
    fmt.Printf("exact_match   mean=%.2f  p95=%.2f\n", rep.Mean["exact_match"], rep.P95["exact_match"])
    fmt.Printf("content_similarity mean=%.2f  p95=%.2f\n", rep.Mean["content_similarity"], rep.P95["content_similarity"])

    // CI gate: fail if exact-match accuracy falls below threshold.
    if rep.Mean["exact_match"] < 0.8 {
        log.Fatalf("exact_match regressed: %.2f (threshold 0.80)", rep.Mean["exact_match"])
    }
}
```

Key points:
- `eval.RunEvals` drives any `core.Agent` — pass your agent directly.
- `Concurrency: 2` caps parallel agent executions; omit or set `<=0` for the default of 4.
- All scorers run with `Source = ScorerSourceTest`, so `Sampling.Rate` is ignored — every item is scored.
- `rep.Failed` counts items where the agent returned an error; these items do not contribute to scorer statistics.
- `EvalReport` maps (`Mean`, `Min`, `Max`, `P50`, `P95`) are keyed by scorer ID (`"exact_match"`, `"content_similarity"`, etc.).

---

## See also

- [eval concept](index.md) — execution model, sampling, and persistence overview
- [eval API reference](api.md) — full type and function definitions
