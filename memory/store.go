// memory/store.go
package memory

import "github.com/nevindra/oasis/core"

// ItemStore stores MemoryItems. Independent of core.Store, which continues
// to handle conversation messages and threads. Satellite stores in this
// repo (store/sqlite, store/postgres) implement both interfaces against
// separate tables.
//
// Deprecated: use core.MemoryItemStore directly. Will be removed in next major.
type ItemStore = core.MemoryItemStore

// Filter selects MemoryItems for read or delete queries.
//
// Deprecated: use core.MemoryFilter directly. Will be removed in next major.
type Filter = core.MemoryFilter

// Store is the union of core.Store (conversation history) and ItemStore
// (memory items). Satellite stores implement both; the developer passes
// one object to memory.WithStore.
type Store interface {
	core.Store
	ItemStore
}
