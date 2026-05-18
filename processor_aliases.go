package oasis

import "github.com/nevindra/oasis/processor"

// ProcessorChain is retained at root as an alias to processor.Chain for
// backward compatibility during the Phase 0 migration. New code should use
// processor.Chain directly.
type ProcessorChain = processor.Chain

// NewProcessorChain creates an empty processor chain.
// Equivalent to processor.NewChain.
func NewProcessorChain() *ProcessorChain { return processor.NewChain() }
