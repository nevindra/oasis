// memory/workingmemory.go
package memory

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/nevindra/oasis/core"
)

// WorkingMemoryID returns the deterministic ID for an agent's working
// memory item in a given scope. The same (agentName, scope) always yields
// the same ID so Upsert overwrites the single canonical row.
func WorkingMemoryID(agentName string, sc core.MemoryScope) string {
	h := sha256.New()
	h.Write([]byte(agentName))
	h.Write([]byte("|"))
	h.Write([]byte(string(sc.Kind)))
	h.Write([]byte("|"))
	h.Write([]byte(sc.Ref))
	h.Write([]byte("|"))
	h.Write([]byte("working-memory"))
	return hex.EncodeToString(h.Sum(nil))
}
