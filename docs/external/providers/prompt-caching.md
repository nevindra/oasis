# Prompt Caching

## TL;DR

Oasis automatically marks two cache breakpoints on every LLM call — the system prompt (with tools) and the current tail of the conversation. Providers that support ephemeral caching (Anthropic, Qwen via OpenAI-compat, anything else that honours `cache_control: {"type":"ephemeral"}`) cache the matching prefix and charge ~10% on cache reads instead of the full input cost. Providers without cache support are unaffected.

It's on by default. To opt out: `agent.WithoutPromptCaching()`.

## When prompt caching pays off

- Long system prompts (1 KB+) reused across calls.
- Stable tool definitions injected into every request.
- Multi-turn agent loops where conversation history grows.
- Multi-agent setups where the same prefix gets re-sent to a router on every turn.

Single-shot calls with no history and no tools won't benefit much — there's nothing to cache.

## What the loop does for you

On every iteration, the agent loop runs `applyPromptCacheMarkers` just before dispatching the LLM call. It resets every `CacheCheckpoint` flag on the message slice, then — unless `WithoutPromptCaching` is set — marks two messages:

- `messages[0]` — the system message. Carries the system prompt + tool definitions + skill instructions (everything cacheable for the lifetime of an agent run).
- `messages[len-1]` — the current tail. On the next iteration, that message becomes mid-list and serves as a cache hit against the provider's stored prefix from the previous call.

Anthropic supports up to 4 cache breakpoints and picks the longest matching prefix. Two markers per call is the sweet spot — covers system+tools and one moving frontier without blowing past the limit.

## How to verify it's working

The `core.Usage` struct surfaces two cache-related fields:

| Field | Meaning |
|---|---|
| `Usage.CachedTokens` | Tokens served from the cache on this call. Higher is better. |
| `Usage.CacheCreationTokens` | Tokens written to the cache on this call. Costs ~25% extra vs base input; amortised across subsequent reads. |

Both come back populated when you talk to an Anthropic-compatible endpoint. Log them from your agent:

```go
agent.WithHooks(agent.Hooks{
    OnIterationComplete: func(ctx context.Context, i int, snap *agent.IterationSnapshot) (agent.IterationDecision, error) {
        usage := snap.Response.Usage
        slog.Info("llm call",
            "iter", i,
            "input_tokens", usage.InputTokens,
            "cached", usage.CachedTokens,
            "cache_written", usage.CacheCreationTokens,
            "output_tokens", usage.OutputTokens,
        )
        return agent.IterationContinue, nil
    },
})
```

If `CachedTokens > 0` on iteration 2+ and grows roughly in lockstep with conversation length, caching is working.

## Things that break cache hits

A cache hit requires the prefix bytes to match exactly. Anything that varies the prefix kills the hit:

1. **`agent.WithDynamicPrompt`** — by definition varies the system prompt per call. Cache will miss every time. Trade-off: keep dynamic prompts short, or restructure so the dynamic part lives in a user message *after* the cache marker.
2. **Dates/timestamps in the system prompt** — `"Today is 2026-05-24..."`. The byte prefix shifts every midnight. Move time-of-day into a user message.
3. **Per-user IDs in the system prompt** — `"You're talking to user_abc"`. Kills cache across users. Move per-user context into a user message.
4. **Memory-injected RAG chunks** — Oasis injects retrieved context into a separate `<context>...</context>` user message *after* the cache marker. The system prefix stays stable. (If you had upgraded from before this layout: the prompt shape changed; see the migration notes.)
5. **Tool definitions changing** — if you use `agent.WithDynamicTools(...)` and the tool list varies per call, the tool definitions are part of the cached prefix and will miss. Static `agent.WithTools(...)` is cache-friendly.
6. **Anthropic 1024-token minimum** — Claude 3.5 Sonnet requires at least 1024 tokens before the cache breakpoint to engage caching at all. Short system prompts won't cache; pad with detailed tool descriptions or examples.
7. **5-minute server-side TTL** — the cache expires server-side after 5 minutes idle. Continuous traffic refreshes it; an idle agent will miss after a gap.

## Opting out

```go
agent := oasis.NewAgent("assistant", "...", provider,
    agent.WithoutPromptCaching(),
)
```

Use when:
- You need byte-exact control via `openaicompat.WithCacheControl(indices...)` at the provider level.
- Your prompts are privacy-sensitive (A/B test variants that must not share a cache key).
- You're benchmarking the no-cache path.

## Composability with per-call markers

`agent.WithoutPromptCaching()` only disables the *automatic* placement. Provider-level markers still apply:

```go
// Mark message 2 manually, but skip the auto-marker
provider := openaicompat.NewProvider("anthropic", "claude-sonnet-4-5",
    openaicompat.WithBaseURL("https://api.anthropic.com/v1"),
    openaicompat.WithOptions(openaicompat.WithCacheControl(2)),
)
agent := oasis.NewAgent("...", "...", provider, agent.WithoutPromptCaching())
```

When auto-placement is on, the agent's two markers and any per-call markers compose — the same byte gets `cache_control: ephemeral` set once (idempotent), and Anthropic sees the union of breakpoints.

## Provider compatibility

| Provider | Cache support |
|---|---|
| `provider/openaicompat` + Anthropic endpoint (`api.anthropic.com/v1`) | ✅ Full support (`cache_creation_input_tokens`, `cache_read_input_tokens`) |
| `provider/openaicompat` + OpenAI | Partial (`prompt_tokens_details.cached_tokens` only — OpenAI handles its own caching) |
| `provider/openaicompat` + Qwen | ✅ |
| `provider/openaicompat` + other | Depends on whether the endpoint honours the `cache_control` extension |
| `provider/gemini` | Not yet — Gemini context caching uses a different API. `CacheCheckpoint` is silently ignored. |

When Gemini's context caching lands, the same `CacheCheckpoint` field can drive it without API changes for callers.
