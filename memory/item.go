// memory/item.go
package memory

import "github.com/nevindra/oasis/core"

// Memory kind constants. These are convenience aliases for the canonical
// core.MemoryKind values; use core.MemoryKind as the type in new code.
const (
	KindFact       core.MemoryKind = "fact"       // semantic fact about user/world
	KindNote       core.MemoryKind = "note"       // working memory scratchpad
	KindEvent      core.MemoryKind = "event"      // episodic event (happened at a time)
	KindPlaybook   core.MemoryKind = "playbook"   // procedural memory ("when X, do Y")
	KindReflection core.MemoryKind = "reflection" // agent's self-critique
	KindSummary    core.MemoryKind = "summary"    // hierarchical compaction
)

// Memory scope constants. These are convenience aliases for the canonical
// core.MemoryScopeKind values; use core.MemoryScopeKind as the type in new code.
const (
	ScopeThread   core.MemoryScopeKind = "thread"   // visible only inside one thread
	ScopeResource core.MemoryScopeKind = "resource" // visible across all threads of one user/chat
	ScopeAgent    core.MemoryScopeKind = "agent"    // visible across all users for this agent
	ScopeGlobal   core.MemoryScopeKind = "global"   // visible to every agent
)

// Scoped is shorthand for core.MemoryScope{Kind: k, Ref: ref}.
func Scoped(k core.MemoryScopeKind, ref string) core.MemoryScope {
	return core.MemoryScope{Kind: k, Ref: ref}
}
