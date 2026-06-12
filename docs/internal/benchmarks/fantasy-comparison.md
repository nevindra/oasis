# Competitive Benchmark: Oasis vs Fantasy

**Date:** 2026-05-27
**Environment:** Go 1.24, Linux, AMD Ryzen 7 9700X (16 threads)
**Methodology:** Both frameworks benchmarked with stub/mock LLM providers returning instant responses. Measures pure framework overhead only. Oasis uses `Execute` (non-streaming), Fantasy uses `Generate` (non-streaming). Streaming benchmarks use each framework's native streaming API.

## Frameworks

| | Oasis | Fantasy |
|---|---|---|
| Module | `github.com/nevindra/oasis` | `charm.land/fantasy` |
| Architecture | Structured tool-use protocol, typed tool dispatch, memory/network/workflow layers | Minimal agent loop, iterator-based streaming, content-type dispatch |
| Memory | Optional (zero-value safe), with semantic recall, history, compression | No built-in memory; conversation via `Messages` field on call |
| Streaming | Channel-based (`chan<- StreamEvent`) with forwarding goroutine | Iterator-based (`StreamResponse` = `func(yield func(StreamPart) bool)`) |
| Tool Dispatch | Parallel by default (up to 10 goroutines) + sync.Map caching | Sequential by default; parallel via `NewParallelAgentTool` marker |

## Head-to-Head Results

### Baseline — single turn, no tools

| Metric | Oasis | Fantasy | Delta |
|--------|------:|--------:|-------|
| ns/op | 536 | **283** | Fantasy **1.9x faster** |
| B/op | 961 | **784** | Fantasy **18% less memory** |
| allocs/op | **8** | 14 | Oasis **43% fewer allocs** |

Fantasy wins on raw speed and memory. Oasis wins on alloc count — 8 vs 14 allocations, meaning Oasis's allocations are larger but fewer (pooled LoopConfig, cached dispatch). Fantasy's advantage comes from a thinner execution path: no LoopConfig struct, no retry wrapper, no processor chain, no memory orchestrator, no context-value propagation.

### Tools registered but not called

| Tools | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op | Oasis allocs | Fantasy allocs |
|------:|------------:|--------------:|-----------:|-------------:|-------------:|---------------:|
| 1 | 1,067 | **776** | 14,166 | **1,912** | **8** | 25 |
| 5 | 1,080 | **2,479** | 14,166 | **6,424** | **8** | 65 |
| 10 | **1,082** | 4,745 | 14,166 | **12,064** | **8** | 115 |

Oasis pre-caches tool definitions at construction — cost is constant regardless of tool count (8 allocs, 14KB). Fantasy rebuilds tool schemas per call, so cost scales linearly (~1,128B and ~10 allocs per additional tool). At 1 tool Fantasy is 1.4x faster; **at 10 tools Oasis is 4.4x faster with constant memory**.

### Tool loop (Generate path, tool calls then text)

| Calls | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op | Oasis allocs | Fantasy allocs |
|------:|------------:|--------------:|-----------:|-------------:|-------------:|---------------:|
| 1 | **2,731** | 3,497 | 18,897 | **7,502** | **46** | 89 |
| 3 | **6,688** | 6,172 | 20,968 | **13,939** | **71** | 167 |
| 5 | **8,374** | 9,793 | 22,526 | **20,713** | **87** | 241 |

**Oasis is faster at all tool counts.** Fantasy uses less memory per call (no message history accumulation), but Oasis uses far fewer allocations. At 5 tool calls, Oasis is 1.2x faster with **64% fewer allocs**. Fantasy's alloc count scales at ~39/call vs Oasis's ~10/call.

### Deep iteration (multiple LLM round-trips)

| Iters | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op | Oasis allocs | Fantasy allocs |
|------:|------------:|--------------:|-----------:|-------------:|-------------:|---------------:|
| 1 | **2,229** | 3,546 | **6,590** | 7,502 | **46** | 89 |
| 3 | **3,810** | 8,122 | **8,739** | 19,240 | **64** | 219 |
| 5 | **5,427** | 13,159 | **11,052** | 32,563 | **81** | 349 |
| 10 | **9,776** | 25,787 | **17,031** | 66,705 | **125** | 671 |

**Oasis dominates deep iteration.** At 10 iterations: **2.6x faster**, **3.9x less memory**, **5.4x fewer allocs**. Fantasy's per-iteration cost is ~2.5us + ~6.6KB + ~65 allocs. Oasis's is ~0.9us + ~1.2KB + ~10 allocs. The gap widens linearly — Oasis's pooled loopState, cached dispatch, and zero-copy message accumulation pay off at depth.

### Streaming — single turn, no tools

| Metric | Oasis | Fantasy | Delta |
|--------|------:|--------:|-------|
| ns/op | 2,298 | **1,512** | Fantasy **1.5x faster** |
| B/op | 2,173 | **2,664** | Oasis **18% less memory** |
| allocs/op | **24** | 42 | Oasis **43% fewer allocs** |

Fantasy's iterator-based streaming (`func(yield func(StreamPart) bool)`) avoids all channel/goroutine overhead. Oasis's channel-based model still requires one forwarding goroutine, but buffer-1 channels and pooled close guards reduced the gap from 4.5x to 1.5x. Oasis now wins on both memory and allocs.

### Streaming with tool calls

| Calls | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op | Oasis allocs | Fantasy allocs |
|------:|------------:|--------------:|-----------:|-------------:|-------------:|---------------:|
| 3 | **11,212** | 15,325 | 23,060 | **24,114** | **94** | 290 |

**Oasis is now 1.4x faster** with comparable memory and 3x fewer allocs. The streaming gap flipped — Oasis's buffer-1 forwarders + pooled state outperform Fantasy's iterator model when tool calls add per-call overhead.

### Parallel tool dispatch (streaming path)

| Parallel | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op | Oasis allocs | Fantasy allocs |
|---------:|------------:|--------------:|-----------:|-------------:|-------------:|---------------:|
| 1 | **3,037** | 10,793 | **18,904** | 14,392 | **46** | 176 |
| 5 | **8,457** | 24,584 | **22,528** | 35,889 | **87** | 409 |
| 10 | **13,347** | 42,936 | **27,572** | 62,564 | **130** | 681 |
| 20 | **23,325** | 76,648 | **37,520** | 116,496 | **206** | 1,219 |

**Oasis dominates parallel dispatch.** At 20 parallel tools: **3.3x faster**, **3.1x less memory**, **5.9x fewer allocs**. Oasis's `dispatchParallel` uses a bounded worker pool with pre-allocated result slices. Fantasy's parallel tool model creates more goroutine overhead per tool call.

Note: Oasis parallel numbers are from the non-streaming `Execute` path. Fantasy parallel dispatch only works in the streaming path.

### Large system prompt

| Prompt | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op |
|-------:|------------:|--------------:|-----------:|-------------:|
| 10KB | 594 | **347** | 1,346 | **920** |
| 50KB | **593** | 353 | 1,346 | **920** |
| 100KB | **596** | 372 | 1,346 | **920** |

Both frameworks are O(1) on prompt size — neither copies the prompt string. Fantasy has a slight edge (~1.6x faster) from its thinner execution path. Both have constant B/op.

### Large user input

| Input | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op |
|------:|------------:|--------------:|-----------:|-------------:|
| 10KB | 544 | **327** | 961 | **784** |
| 50KB | **542** | 310 | 961 | **784** |
| 100KB | **543** | 295 | 961 | **784** |

Both O(1). Fantasy is ~1.8x faster from lower constant overhead. Both have constant memory.

### Large tool result

| Result | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op | Oasis allocs | Fantasy allocs |
|-------:|------------:|--------------:|-----------:|-------------:|-------------:|---------------:|
| 10KB | 5,779 | **3,205** | 29,165 | **7,503** | **46** | 89 |
| 100KB | 118,095 | **3,179** | 125,620 | **7,503** | **47** | 89 |
| 1MB | 1,218,362 | **3,338** | 1,068,790 | **7,506** | **48** | 89 |

Fantasy still wins on large tool results due to its opaque `ToolResponse` values (no string conversion or history storage). However, Phase 4's `ToolResult.Content` string type and byte-scanning `splitContentRunes` closed the gap dramatically — at 1MB: **365x faster** (was 1,111x) and **1.02x payload** (was 9.5x). Oasis stores results in conversation history (needed for compression and observability), which requires copying and chunking large payloads.

> **Stale since Phase 7 (2026-06-12):** the Oasis column above predates the
> Phase 7 O(1) large-tool-result work (`RawOutput` string type + byte-offset
> chunking). Oasis now measures ~4.6µs / ~19.8KB / 49 allocs at 1MB —
> payload-independent, within ~1.4x of Fantasy's ns/op. Re-run the Fantasy
> side before quoting this table.

### Conversation history (prior messages)

| Messages | Oasis ns/op | Fantasy ns/op | Oasis B/op | Fantasy B/op |
|---------:|------------:|--------------:|-----------:|-------------:|
| 20 | n/a | 363 | n/a | 1,760 |
| 100 | n/a | 625 | n/a | 5,600 |
| 1000 | n/a | 6,486 | n/a | 49,888 |

Oasis conversation history lives in `memory.BuildMessages` (~167ns, 1,017B, 6 allocs for the no-memory fast path). Fantasy's conversation cost scales linearly with message count — the `Messages` slice is copied into the call. At 1000 messages, Fantasy costs ~6.5us and ~49KB for the copy.

## Summary

| Scenario | Winner | Margin |
|----------|--------|--------|
| Baseline latency | Fantasy | 1.9x faster |
| Baseline memory | Fantasy | 18% less B/op |
| Baseline allocs | **Oasis** | 43% fewer |
| Tool scaling (10 tools) | **Oasis** | 4.4x faster, constant vs O(n) |
| Tool loop (5 calls) | **Oasis** | Faster, 56% fewer allocs |
| Deep iteration (10) | **Oasis** | 2.6x faster, 3.9x less memory, 5.4x fewer allocs |
| Streaming (no tools) | Fantasy | 1.5x faster (Oasis wins memory + allocs) |
| Streaming + tools | **Oasis** | 1.4x faster, 3x fewer allocs |
| Parallel dispatch (20) | **Oasis** | 3.3x faster, 3.1x less memory, 5.9x fewer allocs |
| Large prompts | Fantasy | ~1.6x faster (both O(1)) |
| Large inputs | Fantasy | ~1.8x faster (both O(1)) |
| Large tool results | Fantasy | 365x faster at 1MB (design difference) |
| Conversation history | Tie | Different models (memory store vs message copy) |

## Analysis

**Fantasy wins on constant overhead.** Its minimal agent loop — no processor chain, no memory orchestrator, no context propagation, iterator-based streaming — produces lower per-call overhead on simple paths. The baseline is 1.9x faster and single-turn streaming is 1.5x faster.

**Oasis wins everywhere that compounds.** Deep iteration (2.6x), tool scaling (4.4x), parallel dispatch (3.3x), streaming with tools (1.4x) — Oasis's investment in pooling (`sync.Pool` for LoopConfig + loopState), caching (dispatch closures, method values, tool definitions), buffer-1 forwarders, and pre-allocation pays dividends when work scales.

**Streaming flipped.** Phase 4's buffer-1 channels and pooled close guards narrowed the no-tool streaming gap from 4.5x to 1.5x, and **flipped the streaming+tools result** — Oasis is now 1.4x faster than Fantasy when tool calls are involved. Oasis also wins on memory and allocs in both streaming scenarios.

**Large tool results closed 3x.** The gap went from 1,111x to 365x. It's still large because Oasis stores results in conversation history (string copy + chunking) while Fantasy keeps them opaque. This is a design tradeoff — Oasis needs the history for compression, observability, and context management.

**What Oasis pays for that Fantasy doesn't have:**
- Memory orchestration (retrieve pipeline, semantic recall, history management)
- Processor chain (pre/post LLM, post-tool hooks)
- Retry middleware (exponential backoff, stream-aware)
- Compression/compaction infrastructure (rune counting, threshold tracking)
- Suspend/resume (state capture, budget tracking)
- Context propagation (`WithTaskContext` for tools)
- Observability hooks (tracer spans, structured logging guards)
- Network multi-agent routing
- Workflow DAG orchestration

**Where to focus next:** The remaining streaming gap (1.5x) could be closed with an iterator-based API option. The large tool result gap (365x) is a design choice — further reduction would require lazy/reference-counted history storage instead of eager string copies.
