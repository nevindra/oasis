// memory/item.go
package memory

import "github.com/nevindra/oasis/core"

// Kind discriminates the role of a MemoryItem. The framework defines six
// canonical kinds below, but Kind is an open string type — users may define
// their own kinds (e.g. "decision", "hypothesis", "todo-event") and every
// pipeline, tool, and filter operates on them identically.
//
// Deprecated: use core.MemoryKind directly. Will be removed in next major.
type Kind = core.MemoryKind

const (
	KindFact       Kind = "fact"       // semantic fact about user/world
	KindNote       Kind = "note"       // working memory scratchpad
	KindEvent      Kind = "event"      // episodic event (happened at a time)
	KindPlaybook   Kind = "playbook"   // procedural memory ("when X, do Y")
	KindReflection Kind = "reflection" // agent's self-critique
	KindSummary    Kind = "summary"    // hierarchical compaction
)

// ScopeKind is the partition kind for memory visibility.
//
// Deprecated: use core.MemoryScopeKind directly. Will be removed in next major.
type ScopeKind = core.MemoryScopeKind

const (
	ScopeThread   ScopeKind = "thread"   // visible only inside one thread
	ScopeResource ScopeKind = "resource" // visible across all threads of one user/chat
	ScopeAgent    ScopeKind = "agent"    // visible across all users for this agent
	ScopeGlobal   ScopeKind = "global"   // visible to every agent
)

// Scope anchors a MemoryItem to a specific instance of a ScopeKind.
// Example: {Kind: ScopeResource, Ref: "user_123"}.
//
// Deprecated: use core.MemoryScope directly. Will be removed in next major.
type Scope = core.MemoryScope

// Scoped is shorthand for Scope{Kind: k, Ref: ref}.
func Scoped(k ScopeKind, ref string) Scope { return Scope{Kind: k, Ref: ref} }

// Source records provenance — where this MemoryItem came from. Powers
// "where did I learn this" queries and forgetting by source.
//
// Deprecated: use core.MemorySource directly. Will be removed in next major.
type Source = core.MemorySource

// MemoryItem is the universal record type for all memory layers.
// One struct, discriminated by Kind, covering facts, notes, events,
// playbooks, reflections, summaries, and any user-defined kinds.
//
// Content is the canonical text shown to the LLM. If a developer needs
// structured data, they JSON-encode it into Content and decode on read —
// the framework intentionally has no Data field, complying with Oasis's
// "no any at the boundary" rule.
//
// Deprecated: use core.MemoryItem directly. Will be removed in next major.
type MemoryItem = core.MemoryItem

// ScoredItem is a MemoryItem paired with a similarity score, returned
// from semantic search.
//
// Deprecated: use core.ScoredMemoryItem directly. Will be removed in next major.
type ScoredItem = core.ScoredMemoryItem
