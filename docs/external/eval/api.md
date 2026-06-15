# eval API reference

Types in this document live in `github.com/nevindra/oasis/core` and `github.com/nevindra/oasis/agent`. They are **not** re-exported at the root `github.com/nevindra/oasis` package — import `core` and `agent` directly.

```go
import (
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)
```

---

## The Scorer contract

### `core.Scorer`

The single interface every scorer must implement.

```go
type Scorer interface {
    ID() string
    Score(ctx context.Context, run ScorerRun) (Score, error)
}
```

**`ID()`** — returns a stable string identifier for this scorer (e.g. `"keyword-coverage"`, `"relevance-v1"`). Used as the primary key in `ScoreRow` and in `Score.ScorerID`. Must be unique across all scorers attached to an agent.

**`Score(ctx, run)`** — evaluates the run and returns a quality signal. Return a Go error **only** for infrastructure failures (network down, database unavailable). A low-quality output is a low `Value` — never an error. Returning an error causes the scorer to be omitted from `AgentResult.Scores` and the store for this run; it does not crash the agent.

---

### `core.Score`

The result of one scorer evaluating one run.

```go
type Score struct {
    ScorerID string
    Value    float64         // 0.0 (worst) … 1.0 (best)
    Reason   string
    Details  json.RawMessage // optional structured data; nil is valid
}
```

**`Value`** — a normalized quality signal in `[0, 1]`. What "0" and "1" mean is scorer-defined; document the semantics in your scorer's `ID` or alongside your scoring logic.

**`Reason`** — a human-readable explanation of the score. Useful for dashboards and debugging.

**`Details`** — arbitrary JSON your scorer emits for richer inspection. `nil` is valid and common for simple scorers.

---

### `core.ScorerRun`

A read-only snapshot of the agent run passed to every scorer. Shares backing memory with `AgentResult` — treat it as immutable; do not modify any slice or map field.

```go
type ScorerRun struct {
    RunID       string
    Input       string
    Output      string
    Thinking    string
    GroundTruth string
    Context     []string
    Iterations  []IterationTrace
    Steps       []StepTrace
    Expected    *ExpectedTrajectory
    Source      ScorerSource
}
```

On live `agent.Execute` runs (the only scoring path in this release) the runtime
populates `Input`, `Output`, `Thinking`, `Iterations`, `Steps`, and `Source`.
The remaining fields — `RunID`, `GroundTruth`, `Context`, and `Expected` — are
reserved for the planned batch/test runner, where the caller supplies them; they
are empty/`nil` on live runs today.

**`RunID`** — identifier for the agent run. Reserved for the batch/test runner; empty on live runs in this release.

**`Input`** — the input passed to the agent (`AgentTask.Input`).

**`Output`** — the agent's final text response.

**`Thinking`** — extended thinking text, if the model emitted one (empty string otherwise).

**`GroundTruth`** — reference answer for comparison scorers. Reserved for the batch/test runner; empty on live runs in this release.

**`Context`** — additional context strings (e.g. retrieved document chunks) for context-aware scorers. Reserved for the batch/test runner; empty on live runs in this release.

**`Iterations`** — per-LLM-loop-iteration trace data (same as `AgentResult.Iterations`).

**`Steps`** — per-tool-call trace data (same as `AgentResult.Steps`).

**`Expected`** — expected tool-call trajectory for trajectory scorers. Reserved for the batch/test runner; `nil` on live runs in this release.

**`Source`** — `ScorerSourceLive` for runs scored via `agent.Execute` (the only path in this release); `ScorerSourceTest` is reserved for the planned batch runner.

---

### `core.ScorerSource`

Identifies whether a run came from live traffic or a test harness.

```go
type ScorerSource string

const (
    ScorerSourceLive ScorerSource = "LIVE"
    ScorerSourceTest ScorerSource = "TEST"
)
```

Sampling is applied only to `ScorerSourceLive` runs. `ScorerSourceTest` runs are always scored regardless of `Sampling.Rate`.

---

## AgentResult field

### `AgentResult.Scores`

```go
// On core.AgentResult:
Scores []Score
```

Carries the `Score` values from all **inline** scorers for this run. Async scores are not present here — they post-date the return of `Execute` and go to the `ScoreStore` / `ScoreSink` only. The slice is nil (not empty) when no inline scorers ran or all were sampled out.

---

## Attaching scorers: ScorerConfig, ScoreMode, Sampling

### `core.ScorerConfig`

Bundles a scorer with its execution policy. Pass one or more to `agent.WithScorers`.

```go
type ScorerConfig struct {
    Scorer   Scorer
    Mode     ScoreMode
    Sampling Sampling
}
```

Type aliases `agent.ScorerConfig`, `agent.ScoreMode`, and `agent.Sampling` are re-exported from the `agent` package for ergonomics — use whichever import you prefer.

---

### `core.ScoreMode`

Controls when a scorer runs relative to the agent hot path.

```go
type ScoreMode uint8

const (
    ScoreModeAuto   ScoreMode = iota // default
    ScoreModeInline
    ScoreModeAsync
)
```

Aliases: `agent.ScoreModeAuto`, `agent.ScoreModeInline`, `agent.ScoreModeAsync`.

| Value | Behaviour |
|---|---|
| `ScoreModeAuto` | Inline for plain `Scorer`; async for `AsyncScorer`. Default when `Mode` is zero. |
| `ScoreModeInline` | Forces synchronous execution; `Score` must return before `Execute` returns. Blocks the hot path. |
| `ScoreModeAsync` | Forces background execution via the worker pool regardless of `PrefersAsync`. Result does not appear in `AgentResult.Scores`. |

---

### `core.Sampling`

Fractional sampling for live runs.

```go
type Sampling struct {
    Rate float64
}
```

**`Rate`** — fraction of `ScorerSourceLive` runs that are scored.

| Rate | Effect |
|---|---|
| `0` or negative | Always-on (treated as `1.0`) |
| `0 < Rate <= 1` | Scored that fraction of the time |
| Omit the scorer | Stops scoring entirely |

`ScorerSourceTest` runs ignore `Rate` and always score.

---

### `agent.WithScorers`

```go
func WithScorers(scorers ...core.ScorerConfig) AgentOption
```

Attaches one or more scorers to the agent. Scorers run after each `Execute` call according to their `Mode` and `Sampling` settings. Multiple scorers compose independently — each receives the same `ScorerRun` snapshot.

---

## Async scoring

### `core.AsyncScorer`

Optional capability interface. Implement alongside `core.Scorer` to signal that a scorer prefers background execution.

```go
type AsyncScorer interface {
    PrefersAsync() bool
}
```

When `PrefersAsync()` returns `true` and `Mode` is `ScoreModeAuto`, the framework enqueues the scorer into a bounded worker pool instead of running it inline. The queue has a hard capacity ceiling; work is **dropped** (not queued indefinitely) when the pool is saturated. Dropped runs are not retried. The pool drains on `agent.Close`.

A scorer that implements `AsyncScorer` but returns `false` from `PrefersAsync` behaves identically to a plain `Scorer` under `ScoreModeAuto`.

---

## Trajectory types

These types describe the *expected* tool-call sequence for a run. They are consumed by user-written trajectory scorers — the framework does not ship a built-in trajectory scorer yet.

### `core.ExpectedTrajectory`

```go
type ExpectedTrajectory struct {
    Steps    []ExpectedStep
    Strategy TrajectoryMatch
}
```

Carried on a `ScorerRun` via the `Expected` field. Populated by the planned batch/test runner; not set on live `Execute` runs in this release.

---

### `core.ExpectedStep`

```go
type ExpectedStep struct {
    Name string
    Args json.RawMessage // nil = name-only match; non-nil = name + args must match
}
```

One expected tool call. When `Args` is nil, a trajectory scorer should match on tool name only. When `Args` is non-nil, the scorer should compare both name and argument shape.

---

### `core.TrajectoryMatch`

Describes the comparison strategy a trajectory scorer should apply.

```go
type TrajectoryMatch uint8

const (
    ExactMatch        TrajectoryMatch = iota
    OrderedSubset
    UnorderedSubset
    LLMJudgeMatch
)
```

| Constant | Intended semantics |
|---|---|
| `ExactMatch` | Actual steps must equal expected steps exactly (same count, same order) |
| `OrderedSubset` | All expected steps appear in the actual steps, in the same relative order |
| `UnorderedSubset` | All expected steps appear in the actual steps, in any order |
| `LLMJudgeMatch` | An LLM is used to judge whether the trajectory satisfies the expectation |

These are conventions for trajectory scorer authors. The framework enforces no specific algorithm — your scorer reads `run.Expected.Strategy` and applies the comparison logic you choose.

---

## Persistence

### `core.ScoreStore`

Optional capability interface for persisting scores. Both `store/sqlite` and `store/postgres` implement it via a `scores` table created on `Init`. Discovered at runtime — you wire it in explicitly via `agent.WithScoreStore`; it is not auto-detected from the agent's memory store.

```go
type ScoreStore interface {
    SaveScores(ctx context.Context, rows []ScoreRow) error
    ListScores(ctx context.Context, filter ScoreFilter) ([]ScoreRow, error)
    GetScore(ctx context.Context, id string) (ScoreRow, error)
    DeleteScores(ctx context.Context, filter ScoreFilter) (int, error)
}
```

**`SaveScores`** — batch-first write; pass multiple rows in one call to amortize round-trip cost. The runtime fills `ID`, `EntityID`, `EntityType`, and `CreatedAt` before calling this.

**`ListScores`** — returns rows matching `filter`. Zero-value fields in the filter are ignored (match-all). Results are ordered by `CreatedAt` descending.

**`GetScore`** — fetches a single row by its `ID`.

**`DeleteScores`** — deletes rows matching `filter` and returns the count of deleted rows.

---

### `core.ScoreRow`

The persisted form of a score. The runtime fills `ID`, `EntityID`, `EntityType`, and `CreatedAt` automatically.

```go
type ScoreRow struct {
    ID         string
    ScorerID   string
    RunID      string
    EntityID   string          // filled by runtime
    EntityType string          // filled by runtime
    Input      string
    Output     string
    Value      float64
    Reason     string
    Details    json.RawMessage
    Source     ScorerSource
    CreatedAt  time.Time       // filled by runtime
}
```

---

### `core.ScoreFilter`

Restricts which rows `ListScores` and `DeleteScores` operate on. Zero-value fields are ignored (match-all).

```go
type ScoreFilter struct {
    ScorerID string
    EntityID string
    Source   ScorerSource
    Since    time.Time
    Limit    int
}
```

| Field | Effect when non-zero |
|---|---|
| `ScorerID` | Only rows from this scorer |
| `EntityID` | Only rows for this entity (agent run group) |
| `Source` | Only rows with this source (`"LIVE"` or `"TEST"`) |
| `Since` | Only rows created at or after this time |
| `Limit` | Return at most this many rows (0 = no limit) |

---

### `agent.WithScoreStore`

```go
func WithScoreStore(store core.ScoreStore) AgentOption
```

Wires a `ScoreStore` into the agent. Required for score persistence. Not auto-detected from `WithStore` — you must pass it explicitly.

Both `*sqlite.Store` and `*postgres.Store` satisfy `core.ScoreStore` after `Init` is called.

---

## Forwarding: ScoreSink

### `core.ScoreSink`

Optional interface for streaming scores to external platforms (analytics pipelines, dashboards, alerting systems).

```go
type ScoreSink interface {
    Emit(ctx context.Context, row ScoreRow) error
}
```

**`Emit`** is called once per score, after the score is computed (and after `SaveScores`, if a `ScoreStore` is also attached). An error from `Emit` is logged and does not affect the agent run or the persisted row.

---

### `agent.WithScoreSink`

```go
func WithScoreSink(sink core.ScoreSink) AgentOption
```

Attaches a `ScoreSink`. Compatible with `WithScoreStore` — both can be active simultaneously. Inline scores are emitted before `Execute` returns; async scores are emitted from the worker pool.

---

## Built-in scorers

All built-in scorers live in package `github.com/nevindra/oasis/eval`. Import it directly — these types are not re-exported at the root `oasis` package.

```go
import "github.com/nevindra/oasis/eval"
```

### Deterministic scorers

Deterministic scorers are **inline by default** — they implement `core.Scorer` and carry no external dependencies.

| Constructor | Measures | ID |
|---|---|---|
| `eval.ExactMatch()` | Output equals GroundTruth (both trimmed) → 1, else 0 | `exact_match` |
| `eval.Contains()` | GroundTruth is a substring of Output → 1, else 0 | `contains` |
| `eval.RegexMatch(re *regexp.Regexp)` | Compiled regexp matches Output → 1, else 0 | `regex_match` |
| `eval.KeywordCoverage(keywords ...string)` | Fraction of keywords present in Output (case-insensitive) | `keyword_coverage` |
| `eval.Completeness(elements ...string)` | Fraction of required elements present in Output (case-insensitive) | `completeness` |
| `eval.ContentSimilarity()` | Token-set Jaccard overlap of Output vs GroundTruth | `content_similarity` |
| `eval.ToolCallAccuracy(expected ...core.ExpectedStep)` | Fraction of expected tool calls (by name) found among actual tool calls | `tool_call_accuracy` |
| `eval.Trajectory(expected core.ExpectedTrajectory)` | Compares actual tool-call sequence to expected via `expected.Strategy`: `ExactMatch`/`OrderedSubset` → 1 or 0; `UnorderedSubset` → fraction | `trajectory` |

### LLM-judge scorers

LLM-judge scorers implement both `core.Scorer` and `core.AsyncScorer` (`PrefersAsync()` returns `true`), so under `ScoreModeAuto` they run off the hot path. Each takes a `core.Provider` that the judge uses to call the LLM.

The tool-call and trajectory judges use distinct IDs (`tool_call_accuracy_llm`, `trajectory_llm`) to avoid colliding with their deterministic counterparts when both are attached to the same agent.

| Constructor | Measures | ID |
|---|---|---|
| `eval.AnswerRelevancy(provider core.Provider)` | How relevant the output is to the input | `answer_relevancy` |
| `eval.Faithfulness(provider core.Provider)` | Whether claims in the output are grounded in the provided context | `faithfulness` |
| `eval.Hallucination(provider core.Provider)` | Presence of fabricated or unsupported information | `hallucination` |
| `eval.AnswerSimilarity(provider core.Provider)` | Semantic similarity of output to ground truth | `answer_similarity` |
| `eval.ContextPrecision(provider core.Provider)` | Fraction of retrieved context that is relevant | `context_precision` |
| `eval.ContextRelevance(provider core.Provider)` | Relevance of provided context to the input | `context_relevance` |
| `eval.Bias(provider core.Provider)` | Presence of demographic or ideological bias | `bias` |
| `eval.Toxicity(provider core.Provider)` | Presence of toxic, harmful, or offensive content | `toxicity` |
| `eval.PromptAlignment(provider core.Provider)` | Adherence to system-prompt instructions | `prompt_alignment` |
| `eval.ToolCallAccuracyLLM(provider core.Provider)` | LLM-judged accuracy of tool call selection and arguments | `tool_call_accuracy_llm` |
| `eval.TrajectoryLLM(provider core.Provider)` | LLM-judged correctness of the full tool-call trajectory | `trajectory_llm` |
| `eval.Rubric(provider core.Provider, criteria string)` | Custom rubric-based quality scoring against caller-supplied criteria | `rubric` |

---

## Batch runner

### `eval.RunEvals`

```go
func RunEvals(ctx context.Context, cfg RunEvalsConfig) (EvalReport, error)
```

Drives any `core.Agent` over a dataset of `EvalItem` records. Returns a non-nil error **only** on context cancellation; individual agent run failures are recorded in `EvalResult.Err` and counted in `EvalReport.Failed` without aborting the batch.

Every run is scored with `Source = ScorerSourceTest`, which bypasses per-scorer `Sampling.Rate` — all scorers fire on every item.

---

### `eval.RunEvalsConfig`

Configuration for a batch evaluation run.

```go
type RunEvalsConfig struct {
    Agent       core.Agent
    Data        []EvalItem
    Scorers     []core.Scorer
    Concurrency int             // <=0 → default 4
    OnItem      func(EvalResult) // optional; called after each item, serialized
}
```

| Field | Description |
|---|---|
| `Agent` | The agent under evaluation; any `core.Agent` implementation. |
| `Data` | The dataset; each item supplies the input and optional reference data. |
| `Scorers` | Scorers to apply to every run. Scorer IDs must be unique. |
| `Concurrency` | Maximum parallel agent runs. Zero or negative → 4. |
| `OnItem` | Optional callback invoked after each item completes; calls are serialized (never concurrent). |

---

### `eval.EvalItem`

One dataset record.

```go
type EvalItem struct {
    Input       string
    GroundTruth string
    Context     []string
    Expected    *core.ExpectedTrajectory
}
```

| Field | Description |
|---|---|
| `Input` | The input string passed to the agent. |
| `GroundTruth` | Reference answer used by comparison scorers (`ExactMatch`, `ContentSimilarity`, `AnswerSimilarity`, …). May be empty for scorers that do not need it. |
| `Context` | Additional context strings (e.g. retrieved document chunks) for context-aware scorers. May be nil. |
| `Expected` | Expected tool-call trajectory for trajectory scorers. May be nil. |

---

### `eval.EvalResult`

The result of running one `EvalItem`.

```go
type EvalResult struct {
    Item   EvalItem
    Result core.AgentResult
    Scores []core.Score
    Err    error
}
```

| Field | Description |
|---|---|
| `Item` | The original item that was evaluated. |
| `Result` | The agent's `AgentResult` for this run; zero value if the agent returned an error. |
| `Scores` | Scores produced by all scorers for this run. |
| `Err` | Non-nil when the agent run itself failed. Scorer results may still be partial. |

---

### `eval.EvalReport`

Aggregate statistics across all items in the dataset.

```go
type EvalReport struct {
    N      int
    Failed int
    Mean   map[string]float64
    Min    map[string]float64
    Max    map[string]float64
    P50    map[string]float64
    P95    map[string]float64
}
```

| Field | Description |
|---|---|
| `N` | Total number of items evaluated. |
| `Failed` | Number of items where the agent run returned an error (`EvalResult.Err != nil`). |
| `Mean` | Per-scorer mean score across all items, keyed by scorer ID. |
| `Min` | Per-scorer minimum score, keyed by scorer ID. |
| `Max` | Per-scorer maximum score, keyed by scorer ID. |
| `P50` | Per-scorer 50th-percentile (median) score, keyed by scorer ID. |
| `P95` | Per-scorer 95th-percentile score, keyed by scorer ID. |

All five maps share the same key space (scorer IDs). A scorer that errored on every item will have its key absent from the maps.
