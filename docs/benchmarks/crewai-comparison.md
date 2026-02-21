# CrewAI vs Oasis — Framework Comparison

> Last updated: 2026-02-21

## Language & Philosophy

| | CrewAI | Oasis |
|---|---|---|
| **Language** | Python (>=3.10) | Go |
| **Dependencies** | Heavy — LiteLLM, Pydantic, ChromaDB/LanceDB, crewai-tools | Minimal — raw `net/http`, no LLM SDKs |
| **Architecture** | Role-based crew metaphor, YAML config + decorators | Interface-driven, single Go module |

---

## Agent Primitives

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Single Agent** | `Agent` with role/goal/backstory, `max_iter=20`, caching, delegation | `LLMAgent` — tool-calling loop with max iterations + forced synthesis | Tie |
| **Multi-Agent** | Crews (sequential/hierarchical), manager agent delegates + reviews | `Network` — LLM router exposes subagents as `agent_*` tools | Tie |
| **Agent Composition** | Crews compose into Flows; no recursive nesting | Recursive — Networks can contain Workflows, Workflows can contain Networks | Oasis |
| **Dynamic Instructions** | YAML variable interpolation, `system_template`/`prompt_template` | `WithDynamicPrompt(PromptFunc)` — per-request prompt/model/tools resolution | Oasis |
| **Runtime Context/DI** | Flow `self.state` (dict or Pydantic), task `context` referencing other outputs | `TaskFromContext(ctx)` — Go `context.Context` propagates through entire call chain, tools read natively | Oasis |
| **Structured Output** | Pydantic `output_pydantic`, `output_json`, `response_format` — runtime validation | `ResponseSchema` / `SchemaObject` — compile-time typed builder, zero runtime cost | Oasis |
| **Code Execution** | `allow_code_execution` — Docker sandbox or restricted fallback; separate `CodeInterpreterTool` | `WithCodeExecution` — sandboxed Python subprocess with tool bridge (`call_tool`, `call_tools_parallel`, `set_result`), no external deps | Oasis |
| **Background Agents** | `async_execution=True` on tasks, `akickoff()` async | `Spawn()` / `AgentHandle` — goroutine-based with lifecycle states (Pending/Running/Completed/Failed/Cancelled) | Oasis |

**Score: CrewAI 0 — Oasis 6 — Tie 2**

---

## Workflow / Orchestration

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Engine** | Dual: Crews (sequential/hierarchical) + Flows (event-driven decorators) — two mental models | DAG-based, single coherent model with automatic concurrent step execution | Oasis |
| **Sequential** | Sequential process or `@listen()` chaining | `After(steps...)` dependency edges | Tie |
| **Parallel** | Multiple `@start()` methods, `or_()`/`and_()` combinators, Pipeline parallel stages | Automatic — steps without dependencies run concurrently | Oasis |
| **Branching** | `@router()` decorator with string labels | `When(fn)` — type-safe Go functions, testable | Oasis |
| **Loops** | Conditional logic in Flows | `ForEach`, `DoWhile`, `DoUntil` with `MaxIter` safety caps | Oasis |
| **Error Handling** | `max_retry_limit`, `guardrail_max_retries`, exception management in tools | `Retry(n, delay)`, `WithOnError`, fail-fast with automatic downstream skip | Oasis |
| **Suspend/Resume** | `@persist` decorator (SQLite), `@human_feedback()`, webhook-based HITL, `crewai replay -t <task_id>` | `Suspend(payload)` / `ErrSuspended.Resume()` with state capture | Tie |
| **Pre-Execution Planning** | `planning=True` — AgentPlanner generates step-by-step plan injected into tasks | N/A | CrewAI |
| **Runtime Definitions** | N/A | `FromDefinition` — JSON-serializable DAG compiled at runtime | Oasis |
| **Plan Execution** | N/A | `WithPlanExecution()` — LLM batches tool calls via `execute_plan` | Oasis |
| **Step Types** | Agent tasks only (sequential or manager-delegated) | `Step`, `AgentStep`, `ToolStep`, `ForEach`, `DoWhile`, `DoUntil` | Oasis |
| **Validation** | Pydantic output validation, guardrail functions/LLM-based | Construction-time (duplicate names, missing deps, cycle detection via Kahn's) | Oasis |

**Score: CrewAI 1 — Oasis 9 — Tie 2**

---

## Tool System

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Definition** | `BaseTool` subclass or `@tool` decorator with Pydantic `args_schema` | `Tool` interface with JSON Schema | Tie |
| **Validation** | Pydantic `BaseModel` schemas, runtime validation with error events | JSON Schema (provider-side) | CrewAI |
| **Parallel Dispatch** | Sequential tool calls per agent; parallel at task/crew level only | Built-in `dispatchParallel` — up to 10 concurrent goroutines per LLM response | Oasis |
| **Caching** | Built-in intelligent caching with custom `cache_function` | N/A | CrewAI |
| **Built-in Tools** | 30+ tools (crewai-tools): file ops, web scraping, search, PDF/DOCX, code interpreter, RAG | Knowledge search, remember, web search, schedule, shell, file, HTTP, data, skill | CrewAI |

**Score: CrewAI 3 — Oasis 1 — Tie 1**

---

## Memory

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Message History** | Unified memory with composite scoring (semantic + recency + importance) | Last N messages + `MaxTokens` trimming | CrewAI |
| **Semantic Recall** | LanceDB vector search with recall modes (shallow/deep), scoped slices | `CrossThreadSearch` — cross-thread semantic search | Tie |
| **User Memory** | Mem0.ai integration, source/privacy tracking | LLM-extracted facts, semantic dedup (0.85), confidence decay, supersession | Oasis |
| **LLM-Powered Analysis** | LLM analyzes content during storage — infers scope, categories, importance | N/A | CrewAI |
| **Consolidation** | Automatic dedup at 0.85 threshold, background threading | Semantic dedup (0.85), confidence decay (0.95/7d), auto-pruning (<0.3 after 30d) | Oasis |
| **Skills** | Training mode (`crewai train`) with human feedback, persisted to `.pkl` files | Database-persisted instruction packages with semantic search, agent self-improvement | Oasis |

**Score: CrewAI 2 — Oasis 3 — Tie 1**

---

## RAG Pipeline

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Chunking Strategies** | Configurable chunk sizes, auto-chunking on ingestion | 4 core (Recursive, Markdown, Semantic, ParentChild) + Flat hierarchy | Oasis |
| **Parent-Child** | N/A | `StrategyParentChild` — match children, return parent context | Oasis |
| **Knowledge Sources** | String, PDF, CSV, Excel, JSON, Text, Docling (web) — Excel + web scraping give it the edge | Text, HTML, Markdown, CSV, JSON, DOCX, PDF | CrewAI |
| **Retrieval** | ChromaDB vector similarity, `results_limit`, `score_threshold`, auto query rewriting | `HybridRetriever` — vector + FTS with RRF fusion + parent-child resolution | Oasis |
| **GraphRAG** | N/A (custom tool integration possible) | `GraphRetriever` — LLM-based ingestion-time extraction, 8 typed relations, persistent `GraphStore`, multi-hop BFS, score blending | Oasis |
| **Reranking** | No built-in reranker; composite scoring in unified memory | `ScoreReranker`, `LLMReranker` | Oasis |
| **Metadata Filtering** | Collection-based scoping (agent role or "crew") | `ChunkFilter` with operators (ByDocument, BySource, ByMeta, CreatedAfter/Before) | Oasis |

**Score: CrewAI 1 — Oasis 6 — Tie 0**

---

## LLM Providers

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Provider Count** | 100+ providers via LiteLLM (OpenAI, Anthropic, Gemini, Groq, Ollama, Bedrock, etc.) | Gemini native + OpenAI-compat (covers OpenAI, Groq, Together, Mistral, Ollama, vLLM, Deepseek, Azure, Anthropic, etc.) | Tie |
| **Integration Style** | SDK-based via LiteLLM — `llm='gpt-4'` string or `LLM()` class, auto-key detection | Interface-driven, explicit construction with base URL | CrewAI |
| **Fallbacks** | Opaque — delegated to LiteLLM layer, limited user control | `WithRetry` decorator — composable, exponential backoff + jitter, stream-aware | Oasis |
| **Rate Limiting** | `max_rpm` on agents/crews (request count only) | `WithRateLimit` decorator (RPM + TPM sliding window) | Oasis |
| **Batch Processing** | `kickoff_for_each()` for input batching; no LLM batch API | `BatchProvider` + `BatchEmbeddingProvider` — native LLM batch APIs | Oasis |
| **Multimodal** | `multimodal=True` boolean flag on agents (v1.9.0+) | `Attachment` struct (MimeType/URL/Data) on input + output, images in chunk metadata | Oasis |
| **SDK Dependencies** | Heavy — LiteLLM pulls openai, anthropic, and other provider SDKs | Raw `net/http` only — minimal deps, full control | Oasis |

CrewAI has a convenience edge with LiteLLM's string-based model selection. Oasis wins on control (composable decorators, no SDKs, proactive rate limiting, native batch APIs, structured multimodal).

**Score: CrewAI 1 — Oasis 5 — Tie 1**

---

## Streaming

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **API** | `stream=True` boolean flag, AG-UI protocol integration | `ExecuteStream(ctx, task, ch)` — channel-based, typed `StreamEvent`, fine-grained control | Oasis |
| **Event Types** | AG-UI protocol: 16 event types (lifecycle, text, tool calls) | `TextDelta`, `ToolCallStart`, `ToolCallResult`, `AgentStart`, `AgentFinish` | CrewAI |
| **SSE** | Via external AG-UI protocol dependency | Built-in `ServeSSE` helper — zero-config, works with any HTTP router | Oasis |
| **Nested Streaming** | Crew-level output streaming | Network emits subagent start/finish events on same channel | Oasis |

**Score: CrewAI 1 — Oasis 3 — Tie 0**

---

## Human-in-the-Loop

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Mechanism** | Task-level `human_input=True`, Flow `@human_feedback()`, webhook-based production HITL | `InputHandler` (LLM-driven `ask_user`) + Suspend/Resume | Tie |
| **LLM-Initiated** | `@human_feedback()` decorator — explicit opt-in per flow step | LLM autonomously decides when to ask via native `ask_user` tool calling | Oasis |
| **Production HITL** | Webhook integration (Slack, Teams, etc.) with auth strategies | Untyped resume payload, framework-agnostic | CrewAI |
| **Persistence** | `@persist` decorator, state survives restarts | State captured in resume closure | CrewAI |

**Score: CrewAI 2 — Oasis 1 — Tie 1**

---

## Processor Pipeline / Guardrails

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Input Processing** | No formal pipeline; Pydantic schemas on tools, task guardrails | `PreProcessor` — modify `ChatRequest` before LLM | Oasis |
| **Output Processing** | Task guardrails — validate-or-retry only (`guardrail_max_retries`) | `PostProcessor` — general-purpose: modify, filter, transform, or replace `ChatResponse` | Oasis |
| **Post-Tool** | `step_callback` after each agent step | `PostToolProcessor` — modify tool results before history | Oasis |
| **LLM Call Hooks** | `before_llm_call`, `after_llm_call` hooks | Pre/PostProcessor covers this | Tie |
| **Built-in Guards** | No-code guardrails (Enterprise), LLM-based quality validation | None built-in (interface only) | CrewAI |
| **Halt** | Guardrail max retries exceeded raises error | `ErrHalt` — graceful early stop from any processor | Oasis |
| **Suspend** | N/A from guardrails | Processors can trigger `Suspend` | Oasis |

**Score: CrewAI 1 — Oasis 5 — Tie 1**

---

## MCP Support

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Client** | Full MCP client — stdio, HTTP, SSE transports; tool filtering (static/dynamic); lifecycle management | N/A | CrewAI |
| **Server** | Community packages (`mcp-crew-ai`), not first-class | MCP server over stdio (JSON-RPC 2.0), tools + resources | Oasis |
| **A2A Protocol** | First-class Agent-to-Agent protocol support (server + client) | N/A | CrewAI |

**Score: CrewAI 2 — Oasis 1 — Tie 0**

---

## Storage

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Relational** | SQLite3 for long-term memory/training data | SQLite (pure-Go), PostgreSQL, libSQL/Turso | Oasis |
| **Vector** | LanceDB (default), ChromaDB (knowledge), configurable: Qdrant, Pinecone, Weaviate, FAISS, pgvector | Integrated in Store (SQLite brute-force, pgvector HNSW, libSQL DiskANN) | CrewAI |
| **Architecture** | Separate storage paths for memory vs knowledge; tightly coupled to subsystems | Unified Store interface (relational + vector + graph in one) | Oasis |

**Score: CrewAI 1 — Oasis 1 — Tie 1**

---

## Observability

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Tracing** | External package (`opentelemetry-instrumentation-crewai`); 30+ event types on event bus | `Tracer` / `Span` interfaces in root package — zero OTEL imports, pluggable backend via `observer.NewTracer()` | Oasis |
| **Structured Logging** | `verbose=True` flag + `output_log_file` (.txt/.json) | Production-grade `slog`-based structured logging throughout core framework | Oasis |
| **Execution Traces** | Event bus with lifecycle events per crew/agent/task/tool | `StepTrace` on `AgentResult` — per-tool name, input, output, tokens, duration (no OTEL required) | Oasis |
| **Span Hierarchy** | Flat event emission per operation | `agent.execute` → `agent.memory.load` / `agent.loop.iteration` → `agent.memory.persist`; `workflow.execute` → `workflow.step` | Oasis |
| **Third-Party Integrations** | Langfuse, MLflow, Arize, SigNoz, Braintrust, Databricks, Wandb | Standard OTEL — any OTEL-compatible backend | CrewAI |
| **Zero Overhead** | Always-on event bus | Nil-check skip — when no tracer configured, all span creation is skipped | Oasis |
| **Cost Tracking** | N/A | Built-in `CostCalculator` with pricing tables for Gemini, OpenAI, Anthropic | Oasis |

**Score: CrewAI 1 — Oasis 6 — Tie 0**

---

## Voice / Audio

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **TTS/STT/STS** | N/A | N/A | Tie |

**Score: Tie 1**

---

## Deployment

| Feature | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Self-hosted** | Python process, Docker, pip/uv | Standard Go binary | Oasis |
| **Managed Cloud** | CrewAI AMP Cloud — one-click deploy, serverless scaling, REST API | N/A | CrewAI |
| **On-Premises Enterprise** | CrewAI Factory — private VPC, AWS/Azure/GCP, RBAC, audit logs | N/A | CrewAI |
| **Dev Playground** | Crew Studio — visual no-code/low-code builder | N/A | CrewAI |
| **API Generation** | Auto-generated REST endpoints for deployed crews | N/A | CrewAI |
| **CLI** | `crewai create`, `crewai run`, `crewai test`, `crewai train`, `crewai replay`, `crewai deploy` | N/A | CrewAI |

**Score: CrewAI 5 — Oasis 1**

---

## Developer Experience

| Aspect | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Getting Started** | CLI scaffolding (`crewai create crew`), YAML config, opinionated defaults — 5 min to first crew | Manual setup, no CLI, no playground | CrewAI |
| **Provider Setup** | `llm='gpt-4'` string — zero config, auto key detection via LiteLLM | Explicit constructor, manual HTTP wiring | CrewAI |
| **Type Safety** | Pydantic schemas — runtime validation + Python type hints (no compile-time guarantees) | Go interfaces — compile-time safety, errors caught before deployment | Oasis |
| **Debugging** | `verbose=True`, event listeners, log files, `flow.plot()` visualization | Channel-based events, `StepTrace` with timing — good but no UI | CrewAI |
| **Documentation** | Polished docs, extensive examples, 100k+ certified developers, community courses | Docs exist but smaller ecosystem, fewer examples | CrewAI |
| **Ecosystem** | 30+ tools, AG-UI, A2A, LiteLLM, multiple vector stores, enterprise platform | Minimal deps, 3 stores, 2 providers — you build what you need | CrewAI |
| **Workflow Authoring** | Dual paradigm: YAML (declarative) + decorators (imperative) — flexible but two mental models | `After`/`When`/`ForEach`/`DoWhile` + compile-time validation | Oasis |
| **Composability** | Crews compose into Flows, but no recursive agent nesting | Everything is `Agent` — recursive composition | Oasis |
| **Boilerplate** | Minimal — YAML config + decorators, Pydantic schemas for tools | Less — interfaces are small, tools are just functions | Oasis |
| **Learning Curve** | Gentle if you know Python; crew metaphor intuitive; two paradigms (Crews + Flows) add cognitive load | Lower if you know Go, higher overall | CrewAI |

**DX verdict: CrewAI 6 — Oasis 4 — Tie 0. CrewAI wins on onboarding, tooling, and ecosystem. Oasis wins on composability, type safety, and less boilerplate once productive.**

---

## Performance

| Aspect | CrewAI | Oasis | Winner |
|---|---|---|---|
| **Concurrency Model** | Python asyncio — single-threaded, GIL-limited; thread wrappers for async | Goroutines — true parallelism, lightweight threads | Oasis |
| **Parallel Tool Calls** | Sequential per agent; parallel at task/crew level via `asyncio.gather` | `dispatchParallel` — up to 10 actual parallel goroutines per LLM response | Oasis |
| **Memory Footprint** | 200-500MB per agent; 2-4GB with LLMs; 16-32GB for 5-10 agents | Go binary ~10-20MB, minimal GC pressure | Oasis |
| **Cold Start** | Slow — Python interpreter + LiteLLM + ChromaDB/LanceDB initialization | Fast — single static binary, sub-second startup | Oasis |
| **Streaming Overhead** | AG-UI protocol layers, LiteLLM abstraction | Direct channel writes, zero abstraction layers | Oasis |
| **Vector Search** | Delegates to ChromaDB/LanceDB/external stores | SQLite: in-process brute-force (no network hop), pgvector: HNSW | Tie |
| **Rate Limiting** | `max_rpm` on agents (request count only) | Proactive sliding-window RPM/TPM — prevents 429s | Oasis |
| **Batch Processing** | `kickoff_for_each` input batching only | `BatchProvider` — native LLM batch API, reduced cost, async processing | Oasis |
| **Plan Execution** | Pre-execution planning (sequential plan) | `execute_plan` — LLM batches parallel tool calls in one turn | Oasis |
| **Deployment Size** | Python env + dependencies (hundreds of MB) | Single binary, ~15-30MB | Oasis |

**Performance verdict: CrewAI 0 — Oasis 9 — Tie 1. Oasis wins decisively.**

---

## Overall Scorecard

| Category | CrewAI | Oasis | Tie |
|---|---|---|---|
| Agent Primitives | 0 | 6 | 2 |
| Workflow / Orchestration | 1 | 9 | 2 |
| Tool System | 3 | 1 | 1 |
| Memory | 2 | 3 | 1 |
| RAG Pipeline | 1 | 6 | 0 |
| LLM Providers | 1 | 5 | 1 |
| Streaming | 1 | 3 | 0 |
| Human-in-the-Loop | 2 | 1 | 1 |
| Processor / Guardrails | 1 | 5 | 1 |
| MCP Support | 2 | 1 | 0 |
| Storage | 1 | 1 | 1 |
| Observability | 1 | 6 | 0 |
| Voice / Audio | 0 | 0 | 1 |
| Deployment | 5 | 1 | 0 |
| Developer Experience | 6 | 4 | 0 |
| Performance | 0 | 9 | 1 |
| **Total** | **27** | **61** | **12** |

---

## Unique to Oasis

- **Scheduler** — polling-based with cron-like schedules (supports Indonesian day names)
- **Batch processing** — `BatchProvider` / `BatchEmbeddingProvider` for reduced-cost offline jobs via native LLM batch APIs
- **Rate limiting decorator** — proactive RPM/TPM sliding window
- **Parent-child chunking** — match small chunks, return large parent context
- **Hybrid retrieval with RRF** — vector + keyword fusion
- **`execute_plan` tool** — LLM batches parallel tool calls in one turn
- **`FromDefinition`** — JSON-serializable workflow definitions compiled at runtime
- **`PostToolProcessor`** — hook after each tool execution
- **Background agents** (`Spawn`/`AgentHandle`) with full lifecycle management
- **Provider decorators** (composable `WithRetry` + `WithRateLimit`)
- **Code execution** (`WithCodeExecution`) — sandboxed Python subprocess with tool bridge (`call_tool`, `call_tools_parallel`, `set_result`)
- **Persistent GraphRAG** — LLM-based ingestion-time edge extraction with 8 typed relations, `GraphStore` in all backends, multi-hop BFS retrieval
- **Deep observability** — `Tracer`/`Span` interfaces in root package (zero OTEL imports), `StepTrace` on every `AgentResult`, hierarchical spans, nil-check zero overhead
- **Cost tracking** — built-in `CostCalculator` with pricing tables for major providers
- **Recursive agent composition** — Networks containing Networks, Workflows containing agent steps containing Networks
- **No SDK dependencies** — raw `net/http` for all LLM communication

## Unique to CrewAI

- **Role-Based Metaphor** — crew/role/goal/backstory mental model deeply embedded in agent identity
- **Hierarchical Process** — auto-created or custom manager agent that delegates, reviews, and validates
- **Pre-Execution Planning** — `planning=True` generates step-by-step plan before agents work
- **Flows with Decorators** — `@start()`, `@listen()`, `@router()`, `@persist`, `@human_feedback()` event-driven workflows
- **Training Mode** — `crewai train -n <iterations>` with human feedback loop, persisted optimizations
- **A2A Protocol** — first-class Agent-to-Agent protocol support (Google's standard)
- **AG-UI Protocol** — standardized agent-to-frontend streaming with 16 event types
- **MCP Client** — full client with stdio/HTTP/SSE transports and tool filtering
- **Task Replay** — `crewai replay -t <task_id>` to resume from specific tasks
- **Tool Caching** — built-in intelligent caching with custom cache functions
- **30+ Built-in Tools** — extensive pre-built tool library (crewai-tools package)
- **LLM-Powered Memory Analysis** — LLM analyzes content during storage, infers scope/categories/importance
- **Webhook HITL** — production-ready webhook integration with auth strategies

---

## Summary

**CrewAI** (27 wins) is a batteries-included Python multi-agent framework — role-based crew metaphor, CLI scaffolding, 30+ tools, AG-UI/A2A protocol support, and a growing ecosystem. Best for Python teams wanting quick multi-agent setup with rich tooling and community.

**Oasis** (61 wins) is a lean, composable Go framework — deeper control over the agent loop, unique workflow primitives (runtime definitions, plan execution, ForEach/DoWhile/DoUntil), sophisticated user memory with decay/supersession, persistent GraphRAG with typed relations, hybrid retrieval, deep observability with zero-overhead tracing, code execution with tool bridge, batch processing, rate limiting, and zero SDK dependencies. Best for teams wanting production-grade Go infrastructure with maximal control and minimal bloat.

---

## Gap Assessment for Oasis

Addressable gaps to close against CrewAI:

1. **Dev playground / CLI** — the single biggest DX gap; CrewAI's `crewai create` and Crew Studio set a high bar
2. **MCP client** — consuming external MCP tools/servers is becoming table stakes; CrewAI has full client support
3. **A2A protocol** — agent-to-agent interop is emerging; CrewAI adopted it early
4. **More built-in tools** — CrewAI ships 30+ vs Oasis's ~10; gap is biggest for web scraping and document-specific tools
5. **Pre-execution planning** — CrewAI's `planning=True` is a useful pattern for complex multi-task crews
