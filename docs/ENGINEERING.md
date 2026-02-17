# Engineering Principles

Mindset dan prinsip yang harus diikuti setiap kontributor (manusia maupun LLM) saat menulis kode di project ini. Dokumen ini bukan tentang formatting atau style -- ini tentang **cara berpikir** saat membuat keputusan engineering.

## Core Philosophy

Oasis menganut prinsip: **simple, fast, intentional**. Setiap baris kode harus punya alasan yang jelas. Kalau ragu antara solusi yang "lebih lengkap" vs "lebih simpel", pilih yang simpel -- sampai terbukti butuh yang lengkap.

## Performance Mindset

### Minimize Allocations

Hindari alokasi yang tidak perlu. Gunakan `strings.Builder` untuk string concatenation, bukan repeated `+`. Reuse slices kalau bisa.

```go
// Good -- satu alokasi, grow sekali
var out strings.Builder
out.Grow(len(items) * 64)
for _, item := range items {
    fmt.Fprintf(&out, "%d. %s\n", i, item.Content)
}

// Bad -- alokasi baru setiap iterasi
result := ""
for _, item := range items {
    result += fmt.Sprintf("%d. %s\n", i, item.Content)
}
```

### Batch Operations

Selalu batch API calls dan database operations kalau bisa. Jangan embed satu-satu kalau bisa embed sekaligus.

```go
// Good -- satu API call untuk semua
texts := make([]string, len(facts))
for i, f := range facts {
    texts[i] = f.Fact
}
embeddings, err := embedding.Embed(ctx, texts)

// Bad -- N API calls
for _, f := range facts {
    emb, err := embedding.Embed(ctx, []string{f.Fact})
    // ...
}
```

### Background Heavy Work

Operasi berat yang bukan di critical path harus di-background. User tidak perlu menunggu embedding dan storage selesai sebelum melihat respons.

```go
// Response sudah terkirim ke user, baru store di background
go func() {
    a.storeMessagePair(ctx, conv.ID, userText, assistantText)
    a.extractAndStoreFacts(ctx, userText, assistantText)
}()
```

### Stream, Don't Buffer

Kalau bisa stream, jangan buffer seluruh response dulu. Oasis men-stream token dari LLM langsung ke user via progressive message editing -- user melihat respons muncul secara bertahap, bukan menunggu 5 detik untuk respons penuh.

### Limit Resource Usage

Selalu set limit/cap yang masuk akal:
- HTTP body: `io.LimitReader(resp.Body, 1<<20)` (1MB)
- Tool output: truncate ke batas yang reasonable (8000 chars)
- Agent iterations: max 10
- Concurrent agents: max 3
- Edit frequency: max 1x/detik (avoid rate limiting)

### Early Return Pattern

Fail fast, return early. Jangan nest logic dalam conditional yang dalam.

```go
// Good -- clear, flat
if err := json.Unmarshal(args, &params); err != nil {
    return ToolResult{Error: "invalid args: " + err.Error()}, nil
}
if params.Query == "" {
    return ToolResult{Error: "query is required"}, nil
}
// ... main logic

// Bad -- deep nesting
if err := json.Unmarshal(args, &params); err == nil {
    if params.Query != "" {
        // ... main logic
    } else {
        return ToolResult{Error: "query is required"}, nil
    }
} else {
    return ToolResult{Error: "invalid args"}, nil
}
```

## Developer Experience Mindset

### Interfaces Sebagai Kontrak

Setiap major component harus diwakili oleh interface. Interface membuat kode testable, swappable, dan jelas tentang kontrak yang diharapkan.

```go
// Di root package -- clean contract
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    ChatWithTools(ctx context.Context, req ChatRequest, tools []ToolDefinition) (ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest, ch chan<- string) (ChatResponse, error)
    Name() string
}

// Implementation di package terpisah
package gemini

type Gemini struct { ... }
func (g *Gemini) Chat(ctx context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) { ... }
```

Verifikasi compile-time bahwa struct mengimplementasi interface:

```go
var _ oasis.VectorStore = (*Store)(nil)
```

### Constructor Injection, Bukan Global State

Dependencies diinject lewat constructor, bukan global variables atau init functions.

```go
// Good -- explicit dependencies
func New(store oasis.VectorStore, emb oasis.EmbeddingProvider) *KnowledgeTool {
    return &KnowledgeTool{store: store, embedding: emb, topK: 5}
}

// Bad -- hidden dependency
var globalStore oasis.VectorStore
func init() { globalStore = sqlite.New("oasis.db") }
```

### Deps Struct untuk Banyak Dependencies

Kalau constructor butuh lebih dari 3-4 parameter, groupkan dalam struct:

```go
type Deps struct {
    Frontend  oasis.Frontend
    ChatLLM   oasis.Provider
    IntentLLM oasis.Provider
    ActionLLM oasis.Provider
    Embedding oasis.EmbeddingProvider
    Store     oasis.VectorStore
    Memory    oasis.MemoryStore
}

func New(cfg *config.Config, deps Deps) *App { ... }
```

### Self-Documenting Code

Nama function, variable, dan type harus jelas tanpa komentar. Komentar hanya untuk **mengapa**, bukan **apa**.

```go
// Good -- nama jelas, komentar menjelaskan "why"
// Overlap ensures semantic continuity across chunk boundaries
func mergeWithOverlap(segments []string, cfg ChunkerConfig) []string { ... }

// Bad -- komentar menjelaskan "what" yang sudah obvious
// This function merges segments
func merge(s []string, c ChunkerConfig) []string { ... }
```

### Package Comment

Setiap package harus punya doc comment di file utamanya:

```go
// Package sqlite implements oasis.VectorStore using pure-Go SQLite
// with in-process brute-force vector search. Zero CGO required.
package sqlite
```

### Error Messages Lowercase, No Period

```go
// Good
return fmt.Errorf("invalid schedule format: %s", schedule)
return ToolResult{Error: "city not found: " + params.City}

// Bad
return fmt.Errorf("Invalid schedule format: %s.", schedule)
```

### Graceful Degradation

Feature yang gagal tidak boleh crash aplikasi. Log error, return fallback, terus jalan.

```go
// Memory context gagal? Tetap jalan tanpa memory.
if a.memory != nil {
    embs, err := a.embedding.Embed(ctx, []string{message})
    if err == nil && len(embs) > 0 {
        mc, err := a.memory.BuildContext(ctx, embs[0])
        if err == nil {
            memoryContext = mc
        }
    }
}
// Lanjut tanpa memoryContext kalau gagal
```

### Tool Errors vs Go Errors

Tool execution selalu "berhasil" di level Go. Error bisnis (input invalid, not found, API gagal) dikembalikan via `ToolResult.Error`, bukan Go `error`. Ini by design -- tool errors harus dikomunikasikan balik ke LLM, bukan propagated sebagai panic.

```go
// Good -- LLM akan melihat error ini dan bisa coba lagi
return oasis.ToolResult{Error: "no results found for: " + query}, nil

// Bad -- ini akan crash/propagate, LLM tidak tahu apa yang salah
return oasis.ToolResult{}, fmt.Errorf("no results found")
```

### Retry yang Benar

Transient errors (429, 5xx, connection drops) harus di-retry dengan exponential backoff. Non-transient errors (400, 404, invalid input) jangan pernah di-retry.

```go
for attempt := 0; attempt <= maxRetries; attempt++ {
    if attempt > 0 {
        delay := time.Duration(1<<(attempt-1)) * time.Second
        time.Sleep(delay)
    }
    result, err := doSomething()
    if err == nil {
        return result, nil
    }
    if !isTransient(err) {
        return result, err  // don't retry non-transient
    }
}
```

## Dependency Philosophy

### Minimal Dependencies

Setiap dependency yang ditambahkan harus punya justifikasi yang kuat. Prefer hand-rolled solution kalau:
- Kode yang dibutuhkan < 200 baris
- Dependency menambahkan banyak transitive deps
- Kita hanya butuh sebagian kecil dari library tersebut

Contoh dari Oasis:
- **No chrono/time library** -- date math di-hand-roll (~50 baris)
- **No LLM SDK** -- semua provider pakai raw HTTP
- **No bot framework** -- Telegram client hand-rolled
- **No error framework** -- 2 custom error types cukup

### No SDK, Raw HTTP

Semua external API (LLM providers, Telegram, Brave Search) diakses via raw HTTP. Ini menghindari version lock-in, mengurangi binary size, dan memberi full control atas request/response handling.

```go
// Direct HTTP -- full control
req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
req.Header.Set("Authorization", "Bearer "+apiKey)
resp, err := client.Do(req)
```

### Dependency Audit

Sebelum menambahkan dependency, tanyakan:
1. Apakah bisa di-hand-roll dalam < 200 baris?
2. Apakah dependency ini well-maintained?
3. Berapa banyak transitive deps yang ditarik?
4. Apakah kita butuh > 30% fitur dari library ini?

Kalau jawaban 1 = ya, atau 4 = tidak, jangan tambahkan dependency.

## Code Organization

### File = Concern

Satu file = satu concern. Jangan campur routing logic dengan storage logic dalam file yang sama.

```
internal/bot/
  app.go      -- struct, constructor, Run()
  router.go   -- message routing
  chat.go     -- chat streaming
  action.go   -- action agent loop
  intent.go   -- intent classification
  store.go    -- background persistence
  agents.go   -- agent lifecycle management
```

### Interface di Root, Implementation di Subdirectory

```
oasis/
  provider.go              -- Provider interface
  provider/gemini/         -- Gemini implementation
  provider/openaicompat/   -- OpenAI-compatible implementation
```

### Package Naming

Package names are short, lowercase, single words. Jangan prefix dengan project name.

```go
// Good
package gemini
package sqlite
package schedule

// Bad
package oasis_gemini
package sqliteStore
package scheduleTool
```

## Testing

### Test Pure Functions

Fokuskan unit test pada pure functions dan business logic, bukan pada integration dengan external services.

```go
func TestComputeNextRun(t *testing.T) {
    now := int64(1739750400) // 2025-02-17 00:00:00 UTC
    next, ok := ComputeNextRun("08:00 daily", now, 7)
    if !ok {
        t.Fatal("expected ok")
    }
    // ...
}
```

### Test di File yang Sama

Test ditempatkan di file `*_test.go` yang berdampingan dengan source file:

```
ingest/
  chunker.go
  chunker_test.go
  pipeline.go
  pipeline_test.go
```

### Table-Driven Tests

Gunakan table-driven tests untuk test cases yang banyak:

```go
func TestDayNameToDOW(t *testing.T) {
    cases := []struct {
        input string
        want  int64
        ok    bool
    }{
        {"monday", 0, true},
        {"senin", 0, true},
        {"invalid", 0, false},
    }
    for _, tc := range cases {
        got, ok := dayNameToDOW(tc.input)
        if ok != tc.ok || got != tc.want {
            t.Errorf("dayNameToDOW(%q) = %d, %v; want %d, %v",
                tc.input, got, ok, tc.want, tc.ok)
        }
    }
}
```

## Summary

| Principle | Implementation |
|-----------|---------------|
| Minimize allocations | `strings.Builder`, reuse slices, pre-allocate |
| Batch operations | Batch embed calls, batch DB writes |
| Background heavy work | `go func()` untuk embed + store setelah response |
| Stream don't buffer | Progressive message editing via channels |
| Limit resources | LimitReader, max iterations, truncation |
| Interface-driven | All major components are Go interfaces |
| Constructor injection | No global state, explicit deps |
| Graceful degradation | Log and continue, don't crash |
| Minimal dependencies | Hand-roll < 200 lines, raw HTTP, no SDKs |
| Early return | Fail fast, flat code, no deep nesting |
| Tool errors in ToolResult | Business errors via ToolResult.Error, not Go error |
| Exponential backoff | Retry transient only, 1s -> 2s -> 4s |
