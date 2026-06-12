# Oasis Framework Benchmarks

Benchmarks measure **framework overhead only** — LLM providers are mocked with instant responses (zero latency). Every nanosecond and byte reported is the framework's tax, not LLM time.

**Environment:** Go 1.26, Linux, AMD Ryzen 7 9700X (16 threads). Results are medians over 3–8 runs (last measured 2026-06-12, post-Phase 6).

## How to Run

```bash
# All benchmarks
go test -run='^$' -bench='.' -benchmem -count=3 ./agent/ ./network/ ./memory/

# Specific package
go test -run='^$' -bench='BenchmarkAgentExecute' -benchmem ./agent/

# Compare against a baseline (install benchstat: go install golang.org/x/perf/cmd/benchstat@latest)
go test -run='^$' -bench='.' -benchmem -count=5 ./agent/ > new.txt
benchstat old.txt new.txt
```

## Agent

The core agent loop: message building, tool dispatch, iteration control, streaming.

| Benchmark | ns/op | B/op | allocs/op | What it measures |
|-----------|------:|-----:|----------:|------------------|
| SingleTurn | 583 | 1,170 | 9 | Bare agent, no tools, one LLM call. **Baseline framework tax.** |
| WithTools/1 | 1,364 | 14,378 | 9 | Tools registered but not called. Definition-building overhead. |
| WithTools/5 | 1,351 | 14,378 | 9 | |
| WithTools/10 | 1,504 | 14,379 | 9 | |
| ToolLoop/calls=1 | 3,802 | 19,672 | 49 | Provider returns tool calls, then text. Full iteration loop. |
| ToolLoop/calls=3 | 7,163 | 22,732 | 76 | |
| ToolLoop/calls=5 | 9,916 | 25,668 | 93 | |
| DeepIteration/iters=1 | 2,350 | 7,359 | 49 | Multiple iterations before final text. Tests iteration scaling. |
| DeepIteration/iters=3 | 4,054 | 11,237 | 70 | |
| DeepIteration/iters=5 | 6,564 | 16,303 | 89 | |
| DeepIteration/iters=10 | 11,044 | 28,205 | 135 | |
| ParallelDispatch/1 | 2,975 | 19,664 | 49 | Parallel tool calls in a single iteration. Goroutine overhead. |
| ParallelDispatch/5 | 9,500 | 25,668 | 93 | |
| ParallelDispatch/10 | 14,702 | 33,534 | 137 | |
| ParallelDispatch/20 | 24,894 | 49,125 | 214 | |
| Stream | 2,654 | 2,382 | 25 | Single turn with streaming channel. |
| StreamWithToolCalls | 11,829 | 24,820 | 99 | Streaming + 3 tool calls. **Real-world hot path.** |
| Processors/1 | 595 | 1,170 | 9 | Pre + post processor chains. |
| Processors/3 | 566 | 1,170 | 9 | |
| Processors/5 | 573 | 1,170 | 9 | |
| LargePrompt/10KB | 598 | 1,554 | 10 | System prompt size scaling. |
| LargePrompt/50KB | 607 | 1,555 | 10 | |
| LargePrompt/100KB | 622 | 1,555 | 10 | |
| LargeInput/10KB | 578 | 1,170 | 9 | User input size scaling. |
| LargeInput/50KB | 581 | 1,170 | 9 | |
| LargeInput/100KB | 558 | 1,170 | 9 | |
| LargeToolResult/10KB | 6,592 | 29,932 | 49 | Tool result payload size scaling. |
| LargeToolResult/100KB | 118,826 | 126,396 | 50 | |
| LargeToolResult/1MB | 1,194,350 | 1,069,932 | 51 | |

> **Phase 6 note:** B/op and allocs/op rose slightly across the board
> (e.g. SingleTurn 961B/8 → 1,170B/9) because `AgentResult` now owns its
> trace memory (`Steps`, `Iterations`, …) instead of aliasing pooled
> backing arrays that the next `Execute` silently overwrote. The old
> numbers were measuring unsafe behavior; ns/op is unchanged.

## Network (Multi-Agent)

Router-based orchestration: tool-definition building, agent delegation, result forwarding.

| Benchmark | ns/op | B/op | allocs/op | What it measures |
|-----------|------:|-----:|----------:|------------------|
| SingleAgent | 3,844 | 21,041 | 74 | One child, one delegation. **Baseline network overhead.** |
| AgentScaling/1 | 4,046 | 21,067 | 75 | Varying child count, router picks one. |
| AgentScaling/3 | 4,285 | 21,549 | 84 | |
| AgentScaling/5 | 4,853 | 22,047 | 91 | |
| AgentScaling/10 | 5,436 | 23,860 | 110 | |
| AgentScaling/20 | 6,828 | 27,502 | 143 | |
| MultiDelegation/1 | 4,152 | 21,084 | 76 | Router delegates to N agents sequentially. |
| MultiDelegation/2 | 5,919 | 23,858 | 113 | |
| MultiDelegation/3 | 7,984 | 26,328 | 150 | |
| MultiDelegation/5 | 11,908 | 32,616 | 220 | |
| Stream | 13,615 | 59,078 | 94 | Single delegation with streaming. |
| LargeAgentOutput/10KB | 11,216 | 31,323 | 75 | Child returns large payload. |
| LargeAgentOutput/100KB | 119,211 | 127,775 | 76 | |
| BuildToolDefs/1 | 8 | 0 | 0 | Cached tool-def lookup. **Zero-alloc when membership stable.** |
| BuildToolDefs/5 | 8 | 0 | 0 | |
| BuildToolDefs/20 | 8 | 0 | 0 | |

## A2A (Agent-to-Agent Protocol)

Protocol layer overhead: JSON-RPC encoding, task lifecycle management, and binary artifact transport. Agents are mocked with instant responses, servers use the bounded in-memory task store, and round-trip measurements use real TCP sockets via httptest loopback — no simulated network latency.

A JSON+base64 loopback round trip necessarily materializes at least three payload-sized buffers (server encode ~1.33x for base64, client body+decode ~1.33x, decoded bytes 1x), so B/op for LargeArtifact is expected at ~4–6x payload size, not 1x.

| Benchmark | ns/op | B/op | allocs/op | What it measures |
|-----------|------:|-----:|----------:|------------------|
| Server_MessageSend | 2,131 | 2,016 | 34 | Handler path: decode → execute → store → encode. **Server baseline tax.** |
| RoundTrip | 37,280 | 17,610 | 198 | Full client→server loopback. Wire cost above the agent execute baseline (~583 ns). |
| RoundTrip_Stream | 109,718 | 126,142 | 338 | Streaming loopback: SSE event translation both directions. |
| RoundTrip_LargeArtifact/10KB | 159,053 | 98,380 | 214 | Binary attachment, 10 KB payload. Base64 wire encoding + decode. |
| RoundTrip_LargeArtifact/100KB | 1,182,483 | 1,188,418 | 243 | |
| RoundTrip_LargeArtifact/1024KB | 10,479,219 | 10,823,324 | 261 | |
| TaskStore | 42 | 0 | 0 | In-memory store under parallel poll. **Zero-alloc read path.** |

## Memory

Message assembly, fact storage, and recall.

| Benchmark | ns/op | B/op | allocs/op | What it measures |
|-----------|------:|-----:|----------:|------------------|
| BuildMessages/20 | 160 | 1,017 | 6 | Message assembly with conversation history. |
| BuildMessages/100 | 174 | 1,017 | 6 | |
| BuildMessages/200 | 209 | 1,017 | 6 | |
| BuildMessages/1000 | 190 | 1,017 | 6 | |
| Remember/facts=1 | 403 | 482 | 4 | Storing memory items. |
| Remember/facts=10 | 2,626 | 3,309 | 34 | |
| Remember/facts=50 | 11,828 | 16,631 | 158 | |
| Recall/items=10 | 51 | 96 | 4 | Retrieving facts (in-memory store). |
| Recall/items=100 | 54 | 96 | 4 | |
| Recall/items=500 | 52 | 96 | 4 | |

## Tool Result Store

The in-memory `ToolResultStore` on the tool-dispatch path, pre-filled with
10,000 unexpired entries (the default cap) — the worst case for the
TTL sweep. The Phase 6 `nextExpiry` watermark skips the O(N) sweep entirely
when nothing can have expired: `Put` was ~376,000 ns/op and `Get` ~87,000
ns/op before the watermark.

| Benchmark | ns/op | B/op | allocs/op | What it measures |
|-----------|------:|-----:|----------:|------------------|
| InMemoryStorePut | 393 | 245 | 2 | Store a tool result with 10,000 entries resident. |
| InMemoryStoreGet | 88 | 0 | 0 | Fetch with 10,000 entries resident. **Zero-alloc read path.** |

## Optimization History

### Phase 6 (current) vs earlier phases vs pre-optimization baseline

**Phase 1** (v0.18.0): channel buffer reduction (64 → 1) + sync.Pool for signaling channel, LoopConfig pass-by-pointer, tool result copy chain reduction, TruncateStr ASCII fast path, RetrieveContext lazy map init, endIter closure inlining.

**Phase 2**: nil-channel ChatStream (zero-alloc non-streaming path), smart message pre-allocation, loopState sync.Pool, cached network tool defs, memory chain caching, AllDefinitions pre-sizing, toolNames elimination, postProcessed lazy alloc, interned iteration strings.

**Phase 3**: slog Enabled() guards, RuneCount skip when compression disabled, LoopConfig sync.Pool, BuildMessages fast path for no-memory agents, cached DispatchFunc at construction, cached method values at Init, retryProvider nil-channel fast path.

**Phase 4**: ToolResult.Content `json.RawMessage` → `string` (eliminates 4 type round-trips on tool results), `splitContentRunes` rewritten with byte-scanning (eliminates `[]rune` explosion), `rawMessageToString`/`toolContentToString` eliminated, streaming forwarder buffer 64→1 (saves 16KB per forwarder), `onceClose` moved to pooled loopState.

**Phase 5**: DX audit — Store interface 25→17 methods (ScheduledActionStore extraction removes 8 methods + 32 mock stubs), iteration.go `iterEndParams` copy elimination (-30 lines), LLM call 3-way branch collapsed, `TextContent` identity function removed, `JSONResult` generic, `WithSemanticTrimming` wired to actual implementation, `WithDecayInterval` stub removed, `RestartOnFail` gains backoff delay, `classifyAgent` correctly returns `KindUnknown` for custom agents, `agentTool.ExecuteRaw` error protocol fixed. Zero agent/memory allocation regression; network allocs -26% from Store interface shrink.

**Phase 6** (current): correctness + audit round — `AgentResult` no longer
aliases pooled `loopState` backing arrays (`Steps`/`Iterations`/`Warnings`/
`Files`/`Sources` ownership transfers to the result on release; previously
the next `Execute` silently overwrote returned results in place). This
costs the allocations the result genuinely owns: SingleTurn 8→9 allocs/op
(+~200B), tool-loop paths +3–10 allocs/op proportional to trace size;
ns/op flat. Tool-result store gained a `nextExpiry` watermark — `Put`/`Get`
no longer run an O(N) TTL sweep per call (~376µs→393ns Put, ~87µs→88ns Get
at the 10,000-entry cap). Also: `OnIterationComplete` snapshot ring-buffer
panic fixed, `Func`/`Erase`/`EraseStreaming` post-execute tails unified
(`Func` now emits `ToolResult.UI`), hand-rolled discard logger replaced
with `slog.DiscardHandler`, dead stream-forwarder symbols removed.

**Agent highlights (memory):**

| Benchmark | Baseline B/op | Phase 1 B/op | Phase 2 B/op | Phase 4 B/op | Total change |
|-----------|------------|-----------|-----------|-----------|-------------|
| SingleTurn | 47,856 | 30,699 | 3,122 | 961 | **-98% (49.8x)** |
| DeepIteration/iters=10 | 242,278 | 42,249 | 20,202 | 17,031 | **-93% (14.2x)** |
| ToolLoop/calls=5 | 78,476 | 43,057 | 24,521 | 22,526 | **-71% (3.5x)** |
| LargeToolResult/1MB | 13,777,170 | 9,549,248 | 9,533,579 | 1,068,790 | **-92% (12.9x)** |
| LargePrompt/100KB | 48,376 | 31,221 | 3,515 | 1,346 | **-97% (35.9x)** |
| LargeInput/100KB | 48,376 | — | 3,131 | 961 | **-98% (50.3x)** |
| Stream | — | — | 40,591 | 2,173 | **-95% (18.7x)** |

**Agent highlights (speed):**

| Benchmark | Baseline ns/op | Phase 1 ns/op | Phase 2 ns/op | Phase 4 ns/op | Total change |
|-----------|-------------|------------|------------|------------|-------------|
| SingleTurn | 4,500 | 3,370 | 1,044 | 536 | **-88% (8.4x)** |
| DeepIteration/iters=10 | 39,100 | 22,060 | 12,352 | 9,776 | **-75% (4.0x)** |
| LargePrompt/100KB | — | — | 39,511 | 596 | **-98% (66.3x)** |
| LargeInput/100KB | — | — | 39,147 | 543 | **-99% (72.1x)** |
| LargeToolResult/1MB | 4,900,000 | 3,820,000 | 3,734,259 | 1,218,362 | **-75% (4.0x)** |
| Stream | — | — | 7,582 | 2,298 | **-70% (3.3x)** |
| StreamWithToolCalls | — | — | 22,782 | 11,212 | **-51% (2.0x)** |

**Agent highlights (allocs):**

| Benchmark | Phase 2 allocs | Phase 4 allocs | Change |
|-----------|---------------|---------------|--------|
| SingleTurn | 30 | 8 | **-73%** |
| ToolLoop/calls=5 | 138 | 87 | **-37%** |
| DeepIteration/iters=10 | 258 | 125 | **-52%** |
| LargePrompt/100KB | 32 | 9 | **-72%** |
| Stream | 44 | 24 | **-45%** |
| LargeToolResult/1MB | 88 | 48 | **-45%** |

**Network highlights:**

| Benchmark | Baseline B/op | Phase 1 B/op | Phase 2 B/op | Phase 5 B/op | Total change |
|-----------|------------|-----------|-----------|-----------|-------------|
| SingleAgent | 72,888 | 37,439 | 22,323 | 20,274 | **-72% (3.6x)** |
| MultiDelegation/5 | 160,203 | 51,503 | 30,066 | 27,372 | **-83% (5.9x)** |
| LargeAgentOutput/100KB | 1,427,880 | 876,584 | 861,427 | 127,016 | **-91% (11.2x)** |
| BuildToolDefs (any count) | ~2,000 | ~2,000 | 0 | 0 | **zero-alloc** |

**Memory highlights:**

| Benchmark | Phase 1 | Phase 2 | Change |
|-----------|---------|---------|--------|
| BuildMessages/20 | 207ns / 1,113B / 9 allocs | 165ns / 1,017B / 6 allocs | **-20% speed, -9% mem, -33% allocs** |

## Key Takeaways

**Baseline overhead is negligible.** A single agent turn costs ~583ns and ~1.2KB with 9 allocs — five orders of magnitude below any real LLM API call (100ms+). The framework is not your bottleneck. Since Phase 6 every returned `AgentResult` owns its trace memory outright (no pooled aliasing), so results are safe to hold indefinitely.

**Everything scales linearly.** No hidden quadratic behavior in iterations, tool dispatch, agent scaling, or delegation chains:
- Each additional tool call: ~1.5us + ~1.5KB
- Each additional iteration: ~1.0us + ~2.3KB
- Each additional parallel dispatch: ~1.2us
- Each additional network delegation: ~1.9us
- Each additional child agent: ~150ns

**Processors add zero measurable overhead.** 1 vs 5 no-op processors show identical numbers — the chain dispatch is essentially free.

**Prompt/input size is now O(1).** With compression disabled (the default), large system prompts and user inputs add zero overhead — 100KB costs the same ~580ns as 0KB. The O(n) RuneCount walk is skipped entirely.

**Tool-result storage is O(1).** The in-memory store's `nextExpiry` watermark skips the TTL sweep unless something can actually have expired: with 10,000 entries resident, `Put` costs 393ns and `Get` 88ns (zero-alloc), down from ~376µs and ~87µs.

**Non-streaming path is zero-overhead.** With nil-channel ChatStream, cached DispatchFunc, and pooled LoopConfig, a non-streaming Execute allocates no channels, spawns no goroutines, and reuses dispatch closures.

**Streaming overhead is minimal.** A streaming Execute adds ~2.1us and 1.2KB over the non-streaming baseline — down from 38KB in Phase 2. Buffer-1 forwarder channels and pooled close guards eliminated the bulk of the streaming tax.

**Large payloads scale at ~1x.** A 1MB tool result costs 1.2ms and 1.04MB (~1x the payload size). The `ToolResult.Content` string type and byte-scanning `splitContentRunes` eliminated the 4× `[]rune` explosion and 4 type round-trips that previously inflated 1MB to 9.5MB.

**Network tool defs are zero-alloc.** Cached dirty-bit pattern means stable networks (no membership changes between calls) pay zero for tool definition building.

**Memory assembly is essentially free.** BuildMessages stays flat from 20 to 1000 messages (~170ns, 6 allocs). The cost lives in the store backend, not the framework.
