# Phase 1 Type-Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate magic strings, raw byte-source ambiguity, and silent failures at the public API boundary by reshaping `AgentTask`, `Attachment`, `Role`, adding `StreamingTool[In, Out]`, renaming `Drain()` → `Close()`, fixing two doc contradictions, and removing two pieces of dead code.

**Architecture:** Nine independent surgical changes to `core/`, `agent/`, `memory/`, and `ingest/` packages, plus call-site migration in tests and one production file (`ingest/image_embed.go`). All changes ship as one breaking minor bump with a single migration guide. The breaking-change budget is spent on type safety only; performance, file splits, and architecture moves are out of scope.

**Tech Stack:** Go 1.24+, stdlib only (encoding/json, encoding/base64), `golangci-lint`, `go test`.

**Source design:** [docs/superpowers/specs/2026-05-18-phase-1-type-safety-design.md](../specs/2026-05-18-phase-1-type-safety-design.md)

**Prerequisites:**
- Branch `migration/microkernel` (current).
- Working tree clean.
- `go test ./...` green at root and each satellite (`mcp/`, `store/sqlite`, `store/postgres`, `provider/gemini`, `provider/openaicompat`, `observer/`, `ingest/`, `sandbox/`, `rag/`).
- `golangci-lint run ./...` clean.

**Verification convention:** After every code-changing task, run from repo root:

```bash
go build ./...
go test ./...
```

For satellite-touching tasks, also run that satellite's tests (see each task's commands). Do not run `golangci-lint` per task — run once at the end of each track.

---

## File map — what each file is responsible for after this plan

Listed only files that change. Files not mentioned are untouched.

**Created**
- (none — all changes modify existing files)

**Modified — `core/`**
- `core/agent.go` — `AgentTask` struct loses `Context map[string]any`, gains typed `ThreadID`/`UserID`/`ChatID`/`Extra` fields. The `Context*` constants and `Task*ID()` accessors are deleted. `With*ID` methods become trivial assignments.
- `core/types.go` — `Attachment` loses `Base64` field, gains three constructors (`NewAttachment`, `NewAttachmentFromURL`, `NewAttachmentFromBase64`). `InlineData()` becomes infallible. `ChatMessage.Role` switches from `string` to `Role`. `Provider.ChatStream` doc updated to say implementations MUST close `ch`. New `Role` type and `RoleSystem`/`RoleUser`/`RoleAssistant`/`RoleTool` constants added.
- `core/tool.go` — Adds `StreamingTool[In, Out]` generic interface.
- `core/erase.go` — Adds `EraseStreaming[In, Out]` function and `erasedStreamingTool` type.
- `core/processor.go` — `ErrHalt` doc clarified to say "return `&ErrHalt{...}`, not value".

**Modified — `agent/`**
- `agent/agent.go` — Drops `contextThreadID`/`contextUserID`/`contextChatID` constants.
- `agent/aliases.go` — Drops `ContextThreadID`/`ContextUserID`/`ContextChatID` re-export constants.
- `agent/agentcore.go` — `Drain()` renamed to `Close() error`. The dead `defer func() { recover() }()` removed from `onceClose`.
- `agent/llm.go` — `subAgentConfig` alias removed.

**Modified — `memory/`**
- `memory/memory_orchestration.go` — `(*AgentMemory).Drain()` renamed to `(*AgentMemory).Close() error`. Internal use of `task.TaskThreadID()`/`task.TaskChatID()` switches to `task.ThreadID`/`task.ChatID` direct field access. `Role: "user"`/`"assistant"` literals switch to `RoleUser`/`RoleAssistant` constants.

**Modified — `ingest/`**
- `ingest/image_embed.go` — Switches from `att.Base64 = ...` to `core.NewAttachmentFromBase64(mime, encoded)`.
- `ingest/extractor_docx.go` — Unchanged (uses `oasis.Image.Base64`, not `Attachment.Base64`; `Image` is a persistence type, not in scope).

**Modified — `network/`**
- `network/network.go` — No changes; only call sites in `network_test.go` touch the renamed surfaces.
- `network/network_test.go` — `Context: map[string]any{...}` literals migrate to typed fields; `Base64:` literals migrate to `NewAttachmentFromBase64`.

**Modified — `provider/`** (satellites — separate `go.mod`)
- `provider/gemini/gemini_test.go` — `Role: "..."` literals optionally migrated to `RoleUser` etc. (compile-compatible without migration; migrate for autocomplete benefit). `Base64:` literal migrates to `NewAttachmentFromBase64`.
- `provider/openaicompat/body_test.go` — Same as gemini_test.go.

**Modified — `oasis.go` (root umbrella)**
- Drops `ContextThreadID`/`ContextUserID`/`ContextChatID` re-export block.
- Adds `Role` type alias and `RoleSystem`/`RoleUser`/`RoleAssistant`/`RoleTool` re-exports.
- Adds `NewAttachment`/`NewAttachmentFromURL`/`NewAttachmentFromBase64` re-exports.
- Adds `StreamingTool` type alias and `EraseStreaming` function re-export.

**Modified — tests at root & subpackages**
- `core/umbrella_types_test.go` — `Base64`-using tests rewritten to use constructors; tests for new constructors added including error path.
- `agent/agent_test.go` — `TestTaskAccessors`/`TestTaskAccessorsEmptyContext`/`TestTaskAccessorsWrongType` rewritten against typed fields. `Context: map[string]any{...}` literals migrate.
- `agent/agentcore_test.go` — `Drain()` calls become `Close()`. `TestAgentCoreNameDescriptionDrain` renamed.
- `agent/memory_integration_test.go` — `Context: map[string]any{ContextThreadID: ...}` literals migrate. `Base64:` literals migrate. `agent.Drain()` calls migrate. `TestDrainWaitsForPersist` renamed.

---

## Track structure

The design doc identifies four parallel tracks. Tasks below are grouped by track. Tracks A/D have no file overlap with B/C; tracks B and C both touch `core/types.go` in disjoint regions — sequence B before C to avoid merge churn.

**Order to execute:** Track A → Track B → Track C → Track D → final verification.

If executing solo, follow this order. If parallelizing, A and D may run alongside B; C runs after B.

---

## Track A — `AgentTask` metadata restructure

**Design reference:** Decision 1 (findings 1.1.a + 2.2.c)

### Task A1: Replace `AgentTask.Context` with typed fields in `core/agent.go`

**Files:**
- Modify: `core/agent.go` (whole file — restructures `AgentTask` and accessor block)

- [ ] **Step 1: Read current file**

Run: `cat core/agent.go`. Confirm lines 38-107 hold the `AgentTask` struct, `Context*` constants, and `Task*ID()` accessors.

- [ ] **Step 2: Rewrite the `AgentTask` block**

In `core/agent.go`, replace lines 38-107 (the struct, constant block, `With*ID` methods, and `Task*ID` accessors) with:

```go
// AgentTask is the input to an Agent.
type AgentTask struct {
	// Input is the natural language task description.
	Input string
	// Attachments carries optional multimodal content (photos, PDFs, documents, etc.) to pass to the LLM.
	// Providers that support multimodal input will attach these to the user message as inline data.
	// Providers that don't support it will ignore this field.
	Attachments []Attachment
	// ThreadID identifies the conversation thread. Empty when no thread is set.
	// Memory uses this to scope history loading and persistence.
	ThreadID string
	// UserID identifies the end user. Empty when no user is set.
	// Dynamic prompts/models/tools may inspect this for per-user behavior.
	UserID string
	// ChatID identifies the chat/channel for messaging integrations (Telegram, Slack, etc.).
	// Empty when no chat is set.
	ChatID string
	// Extra carries arbitrary app-defined metadata. The framework never reads
	// this map; it is opaque pass-through for dynamic resolvers and processors.
	// Use ThreadID/UserID/ChatID for framework-recognized identifiers.
	Extra map[string]any
}

// WithThreadID sets the conversation thread ID on the task and returns it.
func (t AgentTask) WithThreadID(id string) AgentTask { t.ThreadID = id; return t }

// WithUserID sets the user ID on the task and returns it.
func (t AgentTask) WithUserID(id string) AgentTask { t.UserID = id; return t }

// WithChatID sets the chat/channel ID on the task and returns it.
func (t AgentTask) WithChatID(id string) AgentTask { t.ChatID = id; return t }
```

The `ContextThreadID`/`ContextUserID`/`ContextChatID` constants and the `TaskThreadID`/`TaskUserID`/`TaskChatID` methods are removed entirely. No replacement under a different name.

- [ ] **Step 3: Build core/ alone to confirm syntax**

Run: `go build ./core/...`
Expected: build succeeds. Any errors here are typos in the new struct — fix and retry.

- [ ] **Step 4: Commit (deliberately broken root build)**

This commit is intentionally not green at the repo root — call sites in `memory/`, `agent/`, and tests still reference the old API. Subsequent tasks fix them. Commit anyway so the type change is reviewable in isolation.

```bash
git add core/agent.go
git commit -m "refactor!: replace AgentTask.Context map with typed fields

BREAKING: AgentTask.Context map[string]any is removed. Use the typed
ThreadID/UserID/ChatID fields. Apps with custom keys move them to
AgentTask.Extra. The Task*ID() accessors and Context* constants are deleted.

Resolves:
  - 1.1.a: map mutation bug in WithThreadID/WithUserID/WithChatID
  - 2.2.c: magic-string keys in AgentTask.Context"
```

### Task A2: Migrate `memory/memory_orchestration.go` call sites

**Files:**
- Modify: `memory/memory_orchestration.go:161, 165, 268, 409, 415, 446`

- [ ] **Step 1: Replace accessor calls**

In `memory/memory_orchestration.go`, change each occurrence:
- `task.TaskThreadID()` → `task.ThreadID`
- `task.TaskChatID()` → `task.ChatID`
- `task.TaskUserID()` → `task.UserID` (none currently — verify with grep)

Use Edit's `replace_all` per identifier or one Edit per site. Exact replacements:

```diff
-	threadID := task.TaskThreadID()
+	threadID := task.ThreadID
```

```diff
-	chatID := task.TaskChatID()
+	chatID := task.ChatID
```

```diff
-		core.StringAttr("thread_id", task.TaskThreadID()))
+		core.StringAttr("thread_id", task.ThreadID))
```

- [ ] **Step 2: Verify**

Run: `cd memory && go build ./...`
Expected: build succeeds.

Run: `grep -n "TaskThreadID\|TaskUserID\|TaskChatID" memory/memory_orchestration.go`
Expected: no matches.

- [ ] **Step 3: Commit**

```bash
git add memory/memory_orchestration.go
git commit -m "refactor: migrate memory to typed AgentTask fields"
```

### Task A3: Remove agent-package re-exports of context constants

**Files:**
- Modify: `agent/agent.go:19-24` (drop `contextThreadID`/`contextUserID`/`contextChatID` constants)
- Modify: `agent/aliases.go:126-132` (drop `ContextThreadID`/`ContextUserID`/`ContextChatID` block)

- [ ] **Step 1: Edit agent/agent.go**

Delete lines 19-24 (the constant block and its leading comment):

```diff
-// Context key constants for AgentTask.Context (internal — kept for backward compat with tests).
-const (
-	contextThreadID = core.ContextThreadID
-	contextUserID   = core.ContextUserID
-	contextChatID   = core.ContextChatID
-)
-
```

- [ ] **Step 2: Edit agent/aliases.go**

Delete lines 126-132 (the constant block and leading comment):

```diff
-// --- Agent context keys (re-exported from core for subpackage access) ---
-
-const (
-	ContextThreadID = core.ContextThreadID
-	ContextUserID   = core.ContextUserID
-	ContextChatID   = core.ContextChatID
-)
-
```

- [ ] **Step 3: Commit**

```bash
git add agent/agent.go agent/aliases.go
git commit -m "refactor: drop agent-package re-exports of removed Context* constants"
```

### Task A4: Migrate `agent/` test files to typed `AgentTask` fields

**Files:**
- Modify: `agent/agent_test.go:307-353` (TestTaskAccessors* trio), `agent/agent_test.go:639, 684, 730, 770-771`
- Modify: `agent/memory_integration_test.go:203, 271, 366, 407, 457, 490, 622, 655, 698, 735-736, 780, 823, 862-863, 905, 945, 962, 998, 1041, 1077, 1113, 1153, 1191, 1232-1233, 1266, 1301, 1512`

- [ ] **Step 1: Rewrite `TestTaskAccessors` and friends**

In `agent/agent_test.go`, replace the three accessor tests with field-access tests:

```go
// --- Field access tests ---

func TestTaskFields(t *testing.T) {
	task := AgentTask{
		Input:    "test",
		ThreadID: "thread-1",
		UserID:   "user-42",
		ChatID:   "chat-99",
	}

	if got := task.ThreadID; got != "thread-1" {
		t.Errorf("ThreadID = %q, want %q", got, "thread-1")
	}
	if got := task.UserID; got != "user-42" {
		t.Errorf("UserID = %q, want %q", got, "user-42")
	}
	if got := task.ChatID; got != "chat-99" {
		t.Errorf("ChatID = %q, want %q", got, "chat-99")
	}
}

func TestTaskFieldsZero(t *testing.T) {
	task := AgentTask{Input: "test"}

	if task.ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty", task.ThreadID)
	}
	if task.UserID != "" {
		t.Errorf("UserID = %q, want empty", task.UserID)
	}
	if task.ChatID != "" {
		t.Errorf("ChatID = %q, want empty", task.ChatID)
	}
}
```

`TestTaskAccessorsWrongType` is deleted entirely — typed fields make the wrong-type case unrepresentable.

- [ ] **Step 2: Migrate dynamic-resolver tests at lines 639, 684, 730, 770-771**

Replace each:
- `task.TaskUserID()` → `task.UserID`
- `task.Context["tier"]` → `task.Extra["tier"]`

Around line 684:
```diff
-			if task.Context["tier"] == "pro" {
+			if task.Extra["tier"] == "pro" {
```

At lines 770-771:
```diff
-	if got.TaskUserID() != "u1" {
-		t.Errorf("TaskUserID() = %q, want %q", got.TaskUserID(), "u1")
+	if got.UserID != "u1" {
+		t.Errorf("UserID = %q, want %q", got.UserID, "u1")
```

- [ ] **Step 3: Migrate `agent/memory_integration_test.go` Context literals**

Every `Context: map[string]any{ContextThreadID: "X"}` becomes `ThreadID: "X"` directly on the struct literal. Sites combining ThreadID + ChatID likewise become two fields. Worked example:

```diff
-	task := AgentTask{
-		Input:   "msg",
-		Context: map[string]any{ContextThreadID: "thread-1"},
-	}
+	task := AgentTask{
+		Input:    "msg",
+		ThreadID: "thread-1",
+	}
```

Combined:
```diff
-		Context: map[string]any{
-			ContextThreadID: "thread-new",
-			ContextChatID:   "chat-42",
-		},
+		ThreadID: "thread-new",
+		ChatID:   "chat-42",
```

Apply to all 24 occurrences listed in the file path block above.

The single comment `// No ContextChatID` at line 824 becomes `// No ChatID`.

- [ ] **Step 4: Build and run agent tests**

Run: `go build ./agent/... && go test ./agent/... -count=1`
Expected: all tests pass. If any fail, the migration of that site has a typo; fix in place.

- [ ] **Step 5: Commit**

```bash
git add agent/agent_test.go agent/memory_integration_test.go
git commit -m "refactor: migrate agent tests to typed AgentTask fields"
```

### Task A5: Migrate `network/network_test.go` call sites

**Files:**
- Modify: `network/network_test.go:76, 82, 95, 111, 125`

- [ ] **Step 1: Replace user/context accessors and map literals**

```diff
-		return "router for " + task.TaskUserID()
+		return "router for " + task.UserID
```

```diff
-		Context: map[string]any{agent.ContextUserID: "bob"},
+		UserID: "bob",
```

```diff
-				gotUserID = task.TaskUserID()
+				gotUserID = task.UserID
```

```diff
-		Context: map[string]any{agent.ContextUserID: "user-99"},
+		UserID: "user-99",
```

```diff
-			if task.Context["tier"] == "pro" {
+			if task.Extra["tier"] == "pro" {
```

- [ ] **Step 2: Build and run network tests**

Run: `go test ./network/... -count=1`
Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add network/network_test.go
git commit -m "refactor: migrate network tests to typed AgentTask fields"
```

### Task A6: Remove `oasis.go` re-exports of removed constants

**Files:**
- Modify: `oasis.go:459-464`

- [ ] **Step 1: Delete the Context constant re-export block**

```diff
-// Context key constants for AgentTask.Context (exported for subpackage access).
-const (
-	ContextThreadID = core.ContextThreadID
-	ContextUserID   = core.ContextUserID
-	ContextChatID   = core.ContextChatID
-)
-
```

- [ ] **Step 2: Build the full repo**

Run: `go build ./...`
Expected: build succeeds at root. If any subpackage still references `ContextThreadID` etc., remove that reference before continuing (track A is the canonical removal).

Run: `go test ./...`
Expected: all root tests pass. Track A is complete when this command is green.

- [ ] **Step 3: Commit**

```bash
git add oasis.go
git commit -m "refactor: drop oasis re-exports of removed Context* constants"
```

---

## Track B — `Attachment` overhaul

**Design reference:** Decision 2 (findings 1.1.c + 1.2.b + 3.10)

### Task B1: Add constructors and rewrite `InlineData()` in `core/types.go`

**Files:**
- Modify: `core/types.go:230-260` (`Attachment` block)
- Modify: `core/types.go:3-11` (imports — drop unused `encoding/base64` only after Step 2 confirms no other use)

- [ ] **Step 1: Write failing constructor tests in `core/umbrella_types_test.go`**

Append to `core/umbrella_types_test.go` (after the existing `TestAttachment_*` tests, around line 201):

```go
func TestNewAttachment(t *testing.T) {
	att := NewAttachment("image/png", []byte("raw"))
	if att.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want %q", att.MimeType, "image/png")
	}
	if string(att.Data) != "raw" {
		t.Errorf("Data = %q, want %q", att.Data, "raw")
	}
	if att.URL != "" {
		t.Errorf("URL = %q, want empty", att.URL)
	}
}

func TestNewAttachmentFromURL(t *testing.T) {
	att := NewAttachmentFromURL("video/mp4", "https://example.com/v.mp4")
	if att.URL != "https://example.com/v.mp4" {
		t.Errorf("URL = %q, want %q", att.URL, "https://example.com/v.mp4")
	}
	if len(att.Data) != 0 {
		t.Errorf("Data = %v, want empty", att.Data)
	}
}

func TestNewAttachmentFromBase64_OK(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	att, err := NewAttachmentFromBase64("text/plain", encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(att.Data) != "hello" {
		t.Errorf("Data = %q, want %q", att.Data, "hello")
	}
	if att.MimeType != "text/plain" {
		t.Errorf("MimeType = %q, want %q", att.MimeType, "text/plain")
	}
}

func TestNewAttachmentFromBase64_Error(t *testing.T) {
	_, err := NewAttachmentFromBase64("text/plain", "!!!not-base64!!!")
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}
```

- [ ] **Step 2: Run tests to confirm failure**

Run: `go test ./core/... -run "TestNewAttachment" -v`
Expected: `undefined: NewAttachment` / `NewAttachmentFromURL` / `NewAttachmentFromBase64` — compilation fails.

- [ ] **Step 3: Implement constructors and reshape `Attachment` in `core/types.go`**

Replace lines 230-260 with:

```go
// Attachment represents binary content (image, PDF, audio, video, etc.) sent to a multimodal LLM.
// The MimeType determines how the provider interprets the data.
//
// Populate URL for remote references (pre-uploaded to storage/CDN) or Data for
// transient inline bytes. Providers resolve the best transport: URL > Data.
//
// Construct via NewAttachment, NewAttachmentFromURL, or NewAttachmentFromBase64
// to surface decode errors at construction time rather than at provider call time.
type Attachment struct {
	MimeType string `json:"mime_type"`
	URL      string `json:"url,omitempty"`
	// Data carries raw inline bytes. encoding/json marshals []byte as a
	// base64 string on the wire, so JSON round-trips preserve binary content.
	Data []byte `json:"data,omitempty"`
}

// NewAttachment constructs an Attachment from raw inline bytes.
func NewAttachment(mime string, data []byte) Attachment {
	return Attachment{MimeType: mime, Data: data}
}

// NewAttachmentFromURL constructs an Attachment from a remote URL.
// Providers fetch the resource at request time.
func NewAttachmentFromURL(mime, url string) Attachment {
	return Attachment{MimeType: mime, URL: url}
}

// NewAttachmentFromBase64 decodes a base64-encoded payload into an Attachment.
// Returns an error if the encoded string is not valid base64.
//
// Use this when integrating with a source that hands you base64 (some legacy
// APIs, document extractors). For raw bytes, use NewAttachment directly.
func NewAttachmentFromBase64(mime, encoded string) (Attachment, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Attachment{}, fmt.Errorf("decode base64 attachment: %w", err)
	}
	return Attachment{MimeType: mime, Data: data}, nil
}

// InlineData returns the raw inline bytes, or nil if the attachment only carries a URL.
// Why: callers historically branched on Data vs Base64; constructors now decode
// at construction so this read path is infallible.
func (a Attachment) InlineData() []byte { return a.Data }

// HasInlineData reports whether inline bytes are available.
func (a Attachment) HasInlineData() bool { return len(a.Data) > 0 }
```

The `Base64 string` field, the priority-branch in `InlineData()`, and the OR in `HasInlineData()` are deleted.

`encoding/base64` is still used by the new `NewAttachmentFromBase64` — keep the import.

- [ ] **Step 4: Run the new constructor tests**

Run: `go test ./core/... -run "TestNewAttachment" -v`
Expected: PASS for all four.

- [ ] **Step 5: Update the existing `TestAttachment_*` tests that referenced `Base64`**

In `core/umbrella_types_test.go`:

Delete `TestAttachment_InlineData_FromBase64` (lines 155-162 in original numbering) — its premise is gone.

Delete `TestAttachment_InlineData_DataTakesPriority` (lines 164-174) — the priority branch is removed.

In `TestAttachment_HasInlineData` (lines 183-201), remove the `{"Base64 set", Attachment{Base64: "abc"}, true}` row.

- [ ] **Step 6: Run all core tests**

Run: `go test ./core/... -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add core/types.go core/umbrella_types_test.go
git commit -m "refactor!: replace Attachment.Base64 with constructors

BREAKING: Attachment.Base64 is removed. Replacement:
  - NewAttachment(mime, data)        — raw bytes
  - NewAttachmentFromURL(mime, url)  — remote reference
  - NewAttachmentFromBase64(mime, s) — decode base64, surface error

InlineData() is now infallible and returns Data directly. The Data > Base64
priority branch is gone. JSON round-trips still preserve binary content
because encoding/json marshals []byte as base64 on the wire.

Resolves:
  - 1.1.c: silent base64 decode swallow in InlineData
  - 1.2.b: three byte sources / half-deprecated Base64
  - 3.10:  no constructor for the common case"
```

### Task B2: Migrate `ingest/image_embed.go` to constructor

**Files:**
- Modify: `ingest/image_embed.go:43-53`

- [ ] **Step 1: Switch the Attachment construction path**

```diff
-	for i, entry := range images {
-		att := oasis.Attachment{
-			MimeType: entry.image.MimeType,
-		}
-		if entry.image.Base64 != "" {
-			att.Base64 = entry.image.Base64
-		}
-		inputs[i] = oasis.MultimodalInput{
-			Attachments: []oasis.Attachment{att},
-		}
-	}
+	for i, entry := range images {
+		var att oasis.Attachment
+		if entry.image.Base64 != "" {
+			a, err := oasis.NewAttachmentFromBase64(entry.image.MimeType, entry.image.Base64)
+			if err != nil {
+				return nil, fmt.Errorf("decode image %d base64: %w", i, err)
+			}
+			att = a
+		} else {
+			att = oasis.NewAttachment(entry.image.MimeType, nil)
+		}
+		inputs[i] = oasis.MultimodalInput{
+			Attachments: []oasis.Attachment{att},
+		}
+	}
```

Why both branches: `entry.image.Base64` may be empty for images whose payload was stored elsewhere; preserve current behavior of producing an attachment with only `MimeType` set in that case.

- [ ] **Step 2: Build and run the satellite**

Run: `cd ingest && go build ./... && go test ./... -count=1`
Expected: build and tests pass.

- [ ] **Step 3: Commit**

```bash
git add ingest/image_embed.go
git commit -m "refactor: migrate ingest image embed to NewAttachmentFromBase64"
```

### Task B3: Migrate provider satellite tests

**Files:**
- Modify: `provider/gemini/gemini_test.go:275`
- Modify: `provider/openaicompat/body_test.go:234`

- [ ] **Step 1: Migrate gemini test**

```diff
-				{MimeType: "image/png", Base64: "iVBOR..."},
+				mustAttachmentBase64(t, "image/png", "iVBOR..."),
```

Add a helper at the bottom of the test file (above the closing brace if there is no closing brace, at file end otherwise):

```go
// mustAttachmentBase64 fails the test if base64 decode fails. Used to keep
// test data readable while still routing through the validating constructor.
func mustAttachmentBase64(t *testing.T, mime, encoded string) core.Attachment {
	t.Helper()
	att, err := core.NewAttachmentFromBase64(mime, encoded)
	if err != nil {
		t.Fatalf("decode test attachment: %v", err)
	}
	return att
}
```

If the test file does not already import `"github.com/nevindra/oasis/core"`, add it.

If the literal payload `"iVBOR..."` is not actually valid base64 (the existing test treats it as opaque), change the test data to a real base64 string like `"aGVsbG8="` (which decodes to "hello") — the test's intent is to verify the field carries through, not to verify any particular byte content. Update any downstream assertion against the encoded string accordingly.

- [ ] **Step 2: Migrate openaicompat body_test.go**

Same pattern as gemini — add helper, switch literal.

- [ ] **Step 3: Run satellite tests**

```bash
cd provider/gemini && go test ./... -count=1
cd ../openaicompat && go test ./... -count=1
```
Expected: PASS in both.

- [ ] **Step 4: Commit**

```bash
git add provider/gemini/gemini_test.go provider/openaicompat/body_test.go
git commit -m "refactor: migrate provider tests to NewAttachmentFromBase64"
```

### Task B4: Migrate `network/network_test.go` and `agent/memory_integration_test.go` Base64 literals

**Files:**
- Modify: `network/network_test.go:40`
- Modify: `agent/memory_integration_test.go:515-516`

- [ ] **Step 1: Migrate network_test.go line 40**

```diff
-		{MimeType: "image/jpeg", Base64: "abc123"},
+		mustAttachmentBase64(t, "image/jpeg", "YWJjMTIz"),
```

(`"abc123"` was not valid base64 either — `"YWJjMTIz"` is the base64 of `"abc123"`.)

Add the `mustAttachmentBase64` helper if not already present in the file. Import `core` if not already imported.

- [ ] **Step 2: Migrate memory_integration_test.go lines 515-516**

```diff
-		{MimeType: "image/jpeg", Base64: "abc123"},
-		{MimeType: "application/pdf", Base64: "pdfdata"},
+		mustAttachmentBase64(t, "image/jpeg", "YWJjMTIz"),
+		mustAttachmentBase64(t, "application/pdf", "cGRmZGF0YQ=="),
```

Add the `mustAttachmentBase64` helper to the test file (or a shared `testhelpers_test.go` in the package — there is already an `agent/testhelpers_test.go`, add it there to share).

- [ ] **Step 3: Build and test**

```bash
go test ./network/... ./agent/... -count=1
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add network/network_test.go agent/memory_integration_test.go agent/testhelpers_test.go
git commit -m "refactor: migrate network and agent memory tests to NewAttachmentFromBase64"
```

### Task B5: Re-export constructors from `oasis.go`

**Files:**
- Modify: `oasis.go` — add re-exports near the `Attachment` type alias (around line 359)

- [ ] **Step 1: Add re-export functions**

After `type Attachment = core.Attachment` in `oasis.go`:

```go
// NewAttachment constructs an Attachment from raw inline bytes.
func NewAttachment(mime string, data []byte) Attachment {
	return core.NewAttachment(mime, data)
}

// NewAttachmentFromURL constructs an Attachment from a remote URL.
func NewAttachmentFromURL(mime, url string) Attachment {
	return core.NewAttachmentFromURL(mime, url)
}

// NewAttachmentFromBase64 decodes a base64-encoded payload into an Attachment.
// Returns an error if the encoded string is not valid base64.
func NewAttachmentFromBase64(mime, encoded string) (Attachment, error) {
	return core.NewAttachmentFromBase64(mime, encoded)
}
```

- [ ] **Step 2: Build root**

Run: `go build ./...`
Expected: success.

Run: `go test ./...`
Expected: PASS at the root module. Track B is complete when this is green.

- [ ] **Step 3: Commit**

```bash
git add oasis.go
git commit -m "feat: re-export NewAttachment* constructors from oasis"
```

---

## Track C — Typed `Role` + doc fixes

**Design reference:** Decisions 3, 6, 7 (findings 1.2.c, 1.1.b, 1.2.j)

### Task C1: Add `Role` type and constants in `core/types.go`

**Files:**
- Modify: `core/types.go` around line 219 (insert before `ChatMessage`)

- [ ] **Step 1: Insert the Role declaration**

Before `type ChatMessage struct { ... }`:

```go
// Role is the originator of a chat message.
//
// Defined as a typed string so `msg.Role == "user"` continues to compile (Go
// allows comparing a defined string type to an untyped string literal). JSON
// round-trips are preserved without a custom marshaler.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)
```

- [ ] **Step 2: Change `ChatMessage.Role` field type**

```diff
 type ChatMessage struct {
-	Role        string          `json:"role"` // "system", "user", "assistant", "tool"
+	Role        Role            `json:"role"` // see Role* constants
 	Content     string          `json:"content"`
```

- [ ] **Step 3: Update `ChatMessage` constructors**

In `core/types.go` lines 345-359:

```diff
-func UserMessage(text string) ChatMessage {
-	return ChatMessage{Role: "user", Content: text}
-}
-
-func SystemMessage(text string) ChatMessage {
-	return ChatMessage{Role: "system", Content: text}
-}
-
-func AssistantMessage(text string) ChatMessage {
-	return ChatMessage{Role: "assistant", Content: text}
-}
-
-func ToolResultMessage(callID, content string) ChatMessage {
-	return ChatMessage{Role: "tool", Content: content, ToolCallID: callID}
-}
+func UserMessage(text string) ChatMessage {
+	return ChatMessage{Role: RoleUser, Content: text}
+}
+
+func SystemMessage(text string) ChatMessage {
+	return ChatMessage{Role: RoleSystem, Content: text}
+}
+
+func AssistantMessage(text string) ChatMessage {
+	return ChatMessage{Role: RoleAssistant, Content: text}
+}
+
+func ToolResultMessage(callID, content string) ChatMessage {
+	return ChatMessage{Role: RoleTool, Content: content, ToolCallID: callID}
+}
```

- [ ] **Step 4: Build everything**

Run: `go build ./...`
Expected: succeeds. Existing `msg.Role == "user"` comparisons in `memory/`, `provider/openaicompat/body.go`, `provider/gemini/gemini.go`, `guardrail/`, `agent/loop.go`, etc. compile unchanged because Go permits comparing a defined string type to an untyped string literal.

- [ ] **Step 5: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Migrate `memory/memory_orchestration.go` literal `Role:` assignments**

These are struct literals where Go's type inference picks `string`, then assigns to the `Role`-typed field. They compile, but switching to constants improves autocomplete and matches the codebase pattern.

```diff
-	userMsg := core.ChatMessage{Role: "user", Content: task.Input, Attachments: task.Attachments}
+	userMsg := core.ChatMessage{Role: core.RoleUser, Content: task.Input, Attachments: task.Attachments}
```

Apply similarly to lines 504 (`Role: "user"`) and 524 (`Role: "assistant"`) — these are not `core.ChatMessage` literals but `core.Message` (persistence type, `Role string`) — leave them alone.

Verify by reading lines 500-525:
```bash
sed -n '500,530p' memory/memory_orchestration.go
```
If line 504/524 are `core.Message` (note: not `core.ChatMessage`), do NOT migrate them. The persistence `Role` is still a raw string.

- [ ] **Step 7: Commit**

```bash
git add core/types.go memory/memory_orchestration.go
git commit -m "refactor!: introduce typed Role for ChatMessage

BREAKING (source-level): ChatMessage.Role changes from string to Role. Code
using string literals continues to compile (Go permits comparing a defined
string type to untyped string literals). Existing JSON wire format is
preserved.

Resolves 1.2.c."
```

### Task C2: Fix `Provider.ChatStream` doc

**Files:**
- Modify: `core/types.go:27-41`

- [ ] **Step 1: Update the godoc above `Provider`**

```diff
 // Provider abstracts the LLM backend.
 //
 // Two methods handle all interaction patterns:
 //   - Chat: blocking request/response. When req.Tools is non-empty, the
 //     response may contain ToolCalls that the caller must dispatch.
 //   - ChatStream: like Chat but emits StreamEvent values into ch as content
 //     is generated. When req.Tools is non-empty, emits EventToolCallDelta
-//     events as tool call arguments are generated incrementally. The channel
-//     is NOT closed by the provider — the caller owns its lifecycle.
+//     events as tool call arguments are generated incrementally.
+//     Implementations MUST close ch before returning. Callers (including
+//     the agent loop and ServeSSE) range over ch until close, so a provider
+//     that fails to close the channel deadlocks the caller. This matches
+//     the StreamingAgent.ExecuteStream contract.
 //     Returns the final assembled ChatResponse with complete ToolCalls and Usage.
 type Provider interface {
```

- [ ] **Step 2: Build & test (no behavior change, just doc)**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add core/types.go
git commit -m "docs: clarify Provider.ChatStream channel ownership

Implementations have always closed ch (every provider in the tree does);
the prior doc said the opposite, which would deadlock the agent loop's
range over ch. Align the doc with reality.

Resolves 1.1.b."
```

### Task C3: Fix `ErrHalt` doc

**Files:**
- Modify: `core/processor.go:33-40`

- [ ] **Step 1: Update the godoc above `ErrHalt`**

```diff
-// ErrHalt signals that a processor wants to stop agent execution
-// and return a specific response to the caller. The agent loop catches
-// ErrHalt and returns AgentResult{Output: Response} with a nil error.
+// ErrHalt signals that a processor wants to stop agent execution and return
+// a specific response to the caller.
+//
+// To halt, return a pointer: `return &core.ErrHalt{Response: "..."}`. The
+// `Error()` method has a pointer receiver, so only *ErrHalt satisfies the
+// error interface; a value `ErrHalt{...}` would not match. The agent loop
+// catches *ErrHalt via errors.As and returns AgentResult{Output: Response}
+// with a nil error.
 type ErrHalt struct {
 	Response string
 }
```

- [ ] **Step 2: Build & test**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add core/processor.go
git commit -m "docs: clarify ErrHalt must be returned as pointer

Resolves 1.2.j."
```

### Task C4: Re-export `Role` and constants from `oasis.go`

**Files:**
- Modify: `oasis.go` near the `ChatMessage` alias around line 356

- [ ] **Step 1: Add the re-exports**

After the `ChatMessage` type alias:

```go
// Role is the originator of a chat message. See core.Role.
type Role = core.Role

const (
	RoleSystem    = core.RoleSystem
	RoleUser      = core.RoleUser
	RoleAssistant = core.RoleAssistant
	RoleTool      = core.RoleTool
)
```

- [ ] **Step 2: Build & test**

Run: `go build ./... && go test ./...`
Expected: PASS. Track C is complete when this is green.

- [ ] **Step 3: Commit**

```bash
git add oasis.go
git commit -m "feat: re-export Role and Role* constants from oasis"
```

---

## Track D — `StreamingTool`, `Drain`→`Close`, alias removal, recover removal

**Design reference:** Decisions 4, 5, 8, 9 (findings 1.2.i, 3.5, 2.2.f, 2.2.g)

### Task D1: Add `StreamingTool[In, Out]` generic interface in `core/tool.go`

**Files:**
- Modify: `core/tool.go` (append to file)

- [ ] **Step 1: Write a failing test**

Create or append to `core/erase_test.go`:

```go
// streamingFooIn is a test input.
type streamingFooIn struct {
	Query string `json:"query"`
}

// streamingFooOut is a test output.
type streamingFooOut struct {
	Hits int `json:"hits"`
}

// streamingFooTool implements StreamingTool[streamingFooIn, streamingFooOut].
type streamingFooTool struct{}

func (streamingFooTool) Name() string                 { return "foo" }
func (streamingFooTool) Definition() ToolDefinition   { return ToolDefinition{Name: "foo"} }
func (streamingFooTool) Execute(ctx context.Context, in streamingFooIn) (streamingFooOut, error) {
	return streamingFooOut{Hits: len(in.Query)}, nil
}
func (streamingFooTool) ExecuteStream(ctx context.Context, in streamingFooIn, ch chan<- StreamEvent) (streamingFooOut, error) {
	ch <- StreamEvent{Type: EventToolProgress, Content: "searching"}
	return streamingFooOut{Hits: len(in.Query)}, nil
}

func TestEraseStreaming(t *testing.T) {
	tool := streamingFooTool{}
	any := EraseStreaming[streamingFooIn, streamingFooOut](tool)

	// AnyTool contract.
	if any.Name() != "foo" {
		t.Fatalf("Name() = %q, want foo", any.Name())
	}

	// StreamingAnyTool contract.
	st, ok := any.(StreamingAnyTool)
	if !ok {
		t.Fatal("EraseStreaming result does not satisfy StreamingAnyTool")
	}

	ch := make(chan StreamEvent, 4)
	res, err := st.ExecuteStream(context.Background(), json.RawMessage(`{"query":"hi"}`), ch)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("ToolResult.Error = %q, want empty", res.Error)
	}
	close(ch)
	got := 0
	for ev := range ch {
		if ev.Type == EventToolProgress {
			got++
		}
	}
	if got != 1 {
		t.Errorf("EventToolProgress count = %d, want 1", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./core/... -run TestEraseStreaming -v`
Expected: FAIL — `undefined: EraseStreaming` (and possibly `StreamingTool`).

- [ ] **Step 3: Add `StreamingTool` to `core/tool.go`**

Append to `core/tool.go`:

```go
// StreamingTool is the type-safe streaming-capable tool interface. A tool that
// satisfies StreamingTool[In, Out] also satisfies Tool[In, Out] — non-streaming
// callers can still invoke Execute. Use EraseStreaming to register with the loop.
//
// Why this shape: mirrors the Tool[In, Out] / AnyTool / StreamingAnyTool
// triangle. EraseStreaming is a separate function rather than overloading
// Erase because Go generics on interfaces cannot easily branch on whether T
// also satisfies streaming.
type StreamingTool[In, Out any] interface {
	Tool[In, Out]
	// ExecuteStream runs the tool while emitting StreamEvents into ch.
	// The caller owns ch and closes it; ExecuteStream must not close ch.
	ExecuteStream(ctx context.Context, in In, ch chan<- StreamEvent) (Out, error)
}
```

- [ ] **Step 4: Add `EraseStreaming` to `core/erase.go`**

Append to `core/erase.go`:

```go
// EraseStreaming converts a StreamingTool[In, Out] into a StreamingAnyTool.
// Argument unmarshal errors and result marshal errors land in ToolResult.Error
// (business-error channel) per the contract that Go errors from ExecuteStream
// are reserved for infrastructure failures.
func EraseStreaming[In, Out any](t StreamingTool[In, Out]) StreamingAnyTool {
	return &erasedStreamingTool[In, Out]{tool: t}
}

type erasedStreamingTool[In, Out any] struct {
	tool StreamingTool[In, Out]
}

func (e *erasedStreamingTool[In, Out]) Name() string               { return e.tool.Name() }
func (e *erasedStreamingTool[In, Out]) Definition() ToolDefinition { return e.tool.Definition() }

func (e *erasedStreamingTool[In, Out]) ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var in In
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
	}
	out, err := e.tool.Execute(ctx, in)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	return ToolResult{Content: string(body)}, nil
}

func (e *erasedStreamingTool[In, Out]) ExecuteStream(ctx context.Context, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error) {
	var in In
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
	}
	out, err := e.tool.ExecuteStream(ctx, in, ch)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	return ToolResult{Content: string(body)}, nil
}
```

- [ ] **Step 5: Run the new test**

Run: `go test ./core/... -run TestEraseStreaming -v`
Expected: PASS.

- [ ] **Step 6: Run all core tests**

Run: `go test ./core/... -count=1`
Expected: PASS.

- [ ] **Step 7: Re-export from `oasis.go`**

After the `type Tool[In, Out any] = core.Tool[In, Out]` alias (around line 338):

```go
// StreamingTool re-exports core.StreamingTool for type-safe streaming tools.
type StreamingTool[In, Out any] = core.StreamingTool[In, Out]

// EraseStreaming converts a StreamingTool[In, Out] into a StreamingAnyTool.
// Forwards to core.EraseStreaming.
func EraseStreaming[In, Out any](t core.StreamingTool[In, Out]) core.StreamingAnyTool {
	return core.EraseStreaming(t)
}
```

- [ ] **Step 8: Build & test**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add core/tool.go core/erase.go core/erase_test.go oasis.go
git commit -m "feat: add StreamingTool[In, Out] generic and EraseStreaming

Previously type-safe streaming tool authoring forced users to drop down to
AnyTool. StreamingTool[In, Out] mirrors the Tool[In, Out] shape and
EraseStreaming bridges to StreamingAnyTool for registration.

Resolves 1.2.i."
```

### Task D2: Rename `(*AgentMemory).Drain()` → `Close() error`

**Files:**
- Modify: `memory/memory_orchestration.go:150-154`

- [ ] **Step 1: Rename the method**

```diff
-// drain waits for all in-flight persist goroutines to finish.
-// Called during agent/network shutdown to prevent data loss.
-func (m *AgentMemory) Drain() {
-	m.wg.Wait()
-}
+// Close waits for all in-flight persist goroutines to finish and releases
+// any resources held by the orchestrator. Called during agent/network
+// shutdown to prevent data loss.
+//
+// Returns nil today. The error return is reserved for future flush errors
+// (remote stores, network drains). Locking in the io.Closer-shaped signature
+// now avoids a second breaking change later.
+func (m *AgentMemory) Close() error {
+	m.wg.Wait()
+	return nil
+}
```

- [ ] **Step 2: Build memory package**

Run: `go build ./memory/...`
Expected: succeeds.

- [ ] **Step 3: Commit (root still broken — caller migration follows)**

```bash
git add memory/memory_orchestration.go
git commit -m "refactor!: rename AgentMemory.Drain to Close() error

BREAKING: AgentMemory.Drain() is renamed to Close() error. Returns nil today;
the error return is reserved for future flush failures (remote stores).

Resolves 3.5 (orchestrator layer)."
```

### Task D3: Rename `(*AgentCore).Drain()` → `Close() error` and migrate tests

**Files:**
- Modify: `agent/agentcore.go:135-137`
- Modify: `agent/agent_test.go:897-925` (`TestLLMAgentDrainCompletes`)
- Modify: `agent/agentcore_test.go:77, 82, 93, 412`
- Modify: `agent/memory_integration_test.go:1252-1278, 1308`

- [ ] **Step 1: Rename the wrapper**

In `agent/agentcore.go`:

```diff
-// Drain waits for all in-flight background persist goroutines to finish.
-// Call during shutdown to ensure the last messages are written to the store.
-func (c *AgentCore) Drain() { c.mem.Drain() }
+// Close waits for all in-flight background persist goroutines to finish and
+// releases any memory orchestrator resources. Call during shutdown to ensure
+// the last messages are written to the store.
+//
+// Returns nil today; reserved for future flush errors. Embedders that wrap
+// AgentCore inherit this signature.
+func (c *AgentCore) Close() error { return c.mem.Close() }
```

- [ ] **Step 2: Migrate `agent/agent_test.go`**

In `TestLLMAgentDrainCompletes`:

```diff
-func TestLLMAgentDrainCompletes(t *testing.T) {
+func TestLLMAgentCloseCompletes(t *testing.T) {
```

And inside the test:
```diff
-		agent.Drain()
+		if err := agent.Close(); err != nil {
+			t.Errorf("Close error: %v", err)
+		}
```

The 1-second timeout assertion message in the same test:
```diff
-		t.Fatal("Drain() did not complete within 1 second")
+		t.Fatal("Close() did not complete within 1 second")
```

- [ ] **Step 3: Migrate `agent/agentcore_test.go`**

```diff
-	c.mem.Drain()
+	if err := c.mem.Close(); err != nil {
+		t.Errorf("mem.Close error: %v", err)
+	}
```

```diff
-func TestAgentCoreNameDescriptionDrain(t *testing.T) {
+func TestAgentCoreNameDescriptionClose(t *testing.T) {
```

```diff
-	c.Drain()
+	if err := c.Close(); err != nil {
+		t.Errorf("Close error: %v", err)
+	}
```

At line 412 (inside `TestStartDrainTimeoutDrainsChannel` — the test name itself describes a different concept, the `startDrainTimeout` helper, which is *not* renamed; the helper drains a channel and that meaning of "drain" is correct).
```diff
-	a.Drain() // Should not panic.
+	if err := a.Close(); err != nil { // Should not return error or panic.
+		t.Errorf("Close error: %v", err)
+	}
```

- [ ] **Step 4: Migrate `agent/memory_integration_test.go`**

In `TestDrainWaitsForPersist`:
```diff
-// M1+M2: drain waits for in-flight persist goroutines.
-func TestDrainWaitsForPersist(t *testing.T) {
+// M1+M2: Close waits for in-flight persist goroutines.
+func TestCloseWaitsForPersist(t *testing.T) {
```

```diff
-	// Drain instead of time.Sleep — should block until persist completes.
-	agent.Drain()
+	// Close instead of time.Sleep — should block until persist completes.
+	if err := agent.Close(); err != nil {
+		t.Fatalf("Close error: %v", err)
+	}
```

```diff
-		t.Fatalf("expected at least 2 stored messages after Drain, got %d", len(stored))
+		t.Fatalf("expected at least 2 stored messages after Close, got %d", len(stored))
```

At line 1308:
```diff
-	agent.Drain()
+	if err := agent.Close(); err != nil {
+		t.Fatalf("Close error: %v", err)
+	}
```

- [ ] **Step 5: Build and run agent tests**

Run: `go test ./agent/... -count=1`
Expected: PASS.

- [ ] **Step 6: Verify no stale `Drain` callers remain**

Run: `grep -rn "\.Drain()" --include="*.go" .`
Expected: only matches in `provider/openaicompat/stream_test.go` (channel-draining comments — unrelated), `sandbox/`, `internal/ixd/`, `observer/`, `agent/agentcore.go:startDrainTimeout` (a private helper for *channel* drain, unrelated to lifecycle Drain — keep its name as-is).

If any `(*AgentCore).Drain()` or `(*AgentMemory).Drain()` caller remains, migrate it now.

- [ ] **Step 7: Commit**

```bash
git add agent/agentcore.go agent/agent_test.go agent/agentcore_test.go agent/memory_integration_test.go
git commit -m "refactor!: rename AgentCore.Drain to Close() error and migrate tests

BREAKING: AgentCore.Drain() is renamed to Close() error. Returns nil today;
reserved for future flush errors. The startDrainTimeout helper (which drains
a channel of stream events) keeps its name — different concept.

Resolves 3.5."
```

### Task D4: Remove `subAgentConfig` backcompat alias

**Files:**
- Modify: `agent/llm.go:367-368`

- [ ] **Step 1: Verify no callers**

Run: `grep -rn "subAgentConfig" --include="*.go" .`
Expected: only the declaration line. If any internal use remains, replace with `SubAgentConfig` before the next step.

- [ ] **Step 2: Delete the alias**

```diff
-// subAgentConfig is an alias for SubAgentConfig for backward compatibility.
-type subAgentConfig = SubAgentConfig
-
```

- [ ] **Step 3: Build & test**

Run: `go build ./... && go test ./agent/... -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add agent/llm.go
git commit -m "refactor: drop subAgentConfig backcompat alias

Resolves 2.2.f."
```

### Task D5: Remove dead `recover()` from `onceClose`

**Files:**
- Modify: `agent/agentcore.go:412-420`

- [ ] **Step 1: Edit `onceClose`**

```diff
 func onceClose[T any](ch chan<- T) func() {
 	var once sync.Once
 	return func() {
 		once.Do(func() {
-			defer func() { recover() }()
 			close(ch)
 		})
 	}
 }
```

- [ ] **Step 2: Build & test**

Run: `go test ./agent/... -count=1 -race`
Expected: PASS, including `TestStartDrainTimeoutDrainsChannel`. The `recover()` was unreachable: `sync.Once` ensures `close(ch)` runs exactly once across the closure's lifetime, so a double-close panic from within this helper is structurally impossible.

- [ ] **Step 3: Commit**

```bash
git add agent/agentcore.go
git commit -m "refactor: remove unreachable recover() from onceClose

sync.Once already guarantees close(ch) runs exactly once. The defer recover()
guarded against a panic that cannot occur from this helper.

Resolves 2.2.g."
```

---

## Final verification

### Task FV1: Repo-wide sanity check

- [ ] **Step 1: Build all modules from root**

Run: `go build ./...`
Expected: success.

- [ ] **Step 2: Run all root tests**

Run: `go test ./... -count=1 -race`
Expected: PASS.

- [ ] **Step 3: Run each satellite's tests**

```bash
cd mcp && go test ./... -count=1 && cd ..
cd store/sqlite && go test ./... -count=1 && cd ../..
cd store/postgres && go test ./... -count=1 && cd ../..
cd provider/gemini && go test ./... -count=1 && cd ../..
cd provider/openaicompat && go test ./... -count=1 && cd ../..
cd observer && go test ./... -count=1 && cd ..
cd ingest && go test ./... -count=1 && cd ..
cd sandbox && go test ./... -count=1 && cd ..
cd rag && go test ./... -count=1 && cd ..
```

Expected: PASS in every satellite. If `store/postgres` requires a running Postgres, skip (record skip reason). Document any skipped satellite in the commit message.

- [ ] **Step 4: Run golangci-lint**

Run: `golangci-lint run ./...`
Expected: clean.

- [ ] **Step 5: Grep for stale references**

Run each of the following — every one should return zero non-comment matches:

```bash
grep -rn "ContextThreadID\|ContextUserID\|ContextChatID" --include="*.go" .
grep -rn "TaskThreadID\|TaskUserID\|TaskChatID" --include="*.go" .
grep -rn "AgentTask.*\.Context\[" --include="*.go" .
grep -rn "Attachment{[^}]*Base64:" --include="*.go" .
grep -rn "\.Base64 =" --include="*.go" .
grep -rn "\.Drain()" --include="*.go" . | grep -v "startDrainTimeout\|channel\|stream\|comment"
grep -rn "subAgentConfig" --include="*.go" .
grep -rn "recover() }()" agent/agentcore.go
```

Acceptable remaining matches:
- `agent/agentcore.go` — `startDrainTimeout` (private channel-drain helper, unrelated).
- `core/persistence.go:62` — `Image.Base64` (persistence type, out of scope).
- `ingest/extractor_docx.go` — assigns to `Image.Base64` (out of scope).
- Stream/channel "drain" comments in providers and sandbox.

Investigate anything else.

### Task FV2: Update CHANGELOG.md

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Read CHANGELOG.md to locate `[Unreleased]`**

Run: `head -30 CHANGELOG.md`

- [ ] **Step 2: Append breaking-change notes under `[Unreleased]`**

Add the following blocks (preserve existing entries):

```markdown
### Changed (breaking)
- `AgentTask.Context map[string]any` removed. Use the typed `ThreadID`/`UserID`/`ChatID` fields. App-defined metadata moves to `AgentTask.Extra`. The `ContextThreadID`/`ContextUserID`/`ContextChatID` constants and `TaskThreadID()`/`TaskUserID()`/`TaskChatID()` accessors are deleted.
- `Attachment.Base64` field removed. Construct via `NewAttachment` / `NewAttachmentFromURL` / `NewAttachmentFromBase64`. `InlineData()` is now infallible.
- `ChatMessage.Role` switches from `string` to typed `Role`. String literals still compile; new code should use `RoleSystem` / `RoleUser` / `RoleAssistant` / `RoleTool`.
- `AgentCore.Drain()` and `AgentMemory.Drain()` renamed to `Close() error`. Returns nil today; the error return is reserved for future flush failures.

### Added
- `StreamingTool[In, Out]` generic interface for type-safe streaming tool authoring. Bridge via `EraseStreaming[In, Out]` to register as a `StreamingAnyTool`.
- `NewAttachment`, `NewAttachmentFromURL`, `NewAttachmentFromBase64` constructors.
- `Role` type with `RoleSystem`, `RoleUser`, `RoleAssistant`, `RoleTool` constants.

### Removed
- Dead `subAgentConfig` alias in `agent/llm.go`.
- Unreachable `defer func() { recover() }()` from `onceClose` in `agent/agentcore.go`.

### Fixed
- `Provider.ChatStream` doc no longer claims providers leave the channel open — every implementation closes it, matching the actual contract used by the agent loop.
- `ErrHalt` doc now clarifies that processors must return `&ErrHalt{...}` (pointer), not a value, to satisfy the `error` interface.
- Map-mutation bug in `AgentTask.With*ID` methods (a stale concern made moot by the typed-field migration; documented for migration-guide completeness).
- Silent base64-decode swallow in `Attachment.InlineData()` — moved to construction time via `NewAttachmentFromBase64`.
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog for Phase 1 type-safety release"
```

### Task FV3: Migration guide pointer

The user-facing migration guide already lives in the design doc at `docs/superpowers/specs/2026-05-18-phase-1-type-safety-design.md` (the "Migration guide (user-facing)" section). It does not need duplication. When tagging the release, the release notes should link to that section verbatim.

- [ ] **Step 1: No file change.** Confirm during release that the link in the GitHub release notes points to the design doc's "Migration guide" anchor.

---

## Risk notes for the implementer

- **Source compatibility lie:** `ChatMessage.Role` going from `string` to `Role` *is* technically a source change for any caller that does `var r string = msg.Role`. Direct assignment requires a conversion now. Comparisons (`msg.Role == "user"`) are unaffected because Go permits comparing a defined string type to an untyped literal. If any caller in the satellites does the assignment pattern, expect compile errors during Task C1 and fix at that site (cast: `string(msg.Role)`).
- **`oasis.Image.Base64` is out of scope.** It is a persistence type, not the `Attachment` Base64 we are removing. Do not touch `core/persistence.go` or `ingest/extractor_docx.go`'s `Image{Base64: ...}` literal.
- **The `startDrainTimeout` helper keeps its name.** That helper drains a *channel of events*, not lifecycle resources. The Drain→Close rename applies only to lifecycle methods on `AgentCore` and `AgentMemory`.
- **Tests with implicit `string`→`Role` inference compile after C1.** Struct literals like `ChatMessage{Role: "user"}` work unchanged. The autocomplete benefit of `RoleUser` is real but cosmetic for existing code. Migrate `memory/memory_orchestration.go` literals because it sits in core orchestration; leave provider satellite test literals alone unless a satellite is otherwise being touched.
- **Image embed production migration.** The design doc states "all production code already populates Data" but `ingest/image_embed.go:47-48` does in fact write to `Base64`. Task B2 corrects this. If a benchmark or load test references this hot path, re-run it after B2 to confirm no regression — the base64 decode now happens once at construction instead of zero times at construction plus zero times at read; net change is +1 decode per image.
- **CI gate.** `go test ./...` from root and from every satellite must be green before merge. The "deliberately broken root build" commit in Task A1 is a signpost for reviewers; it is fixed by the end of Track A. If running CI per commit, squash Track A or otherwise resequence so each pushed commit builds.

---

## Spec coverage check

Mapping every design decision to its implementing task(s):

| Design decision | Tasks |
|---|---|
| 1. `AgentTask` metadata restructure (1.1.a, 2.2.c) | A1–A6 |
| 2. `Attachment` overhaul (1.1.c, 1.2.b, 3.10) | B1–B5 |
| 3. Typed `Role` (1.2.c) | C1, C4 |
| 4. `StreamingTool[In, Out]` (1.2.i) | D1 |
| 5. `Drain()` → `Close() error` (3.5) | D2, D3 |
| 6. `Provider.ChatStream` doc (1.1.b) | C2 |
| 7. `ErrHalt` doc (1.2.j) | C3 |
| 8. Remove `subAgentConfig` alias (2.2.f) | D4 |
| 9. Remove dead `recover()` (2.2.g) | D5 |
| Open question 1: enumerate memory drainers | Resolved in D2/D3 — only `AgentMemory.Drain` and `AgentCore.Drain` exist; satellite stores expose their own `Close()` and don't participate. |
| Open question 2: confirm `Base64:` test sites | Resolved in B1, B3, B4 — 6 sites total: `core/umbrella_types_test.go` (2 sites — Step 5 deletes two and edits one row), `provider/gemini/gemini_test.go:275`, `provider/openaicompat/body_test.go:234`, `network/network_test.go:40`, `agent/memory_integration_test.go:515-516`. The production site `ingest/image_embed.go:48` is in B2. |
| Migration guide (user-facing) | Already in design doc; FV3 confirms link. |
| Release sequencing | Tracks A→B→C→D with FV1–FV3 at the end. |
| Testing approach | Each task runs targeted tests; FV1 runs the full matrix. |
