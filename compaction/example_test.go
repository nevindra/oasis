package compaction_test

import (
	"context"
	"fmt"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/compaction"
)

// canonProvider returns a fixed canonical compaction response.
// Real apps pass a configured core.Provider here.
type canonProvider struct{}

func (canonProvider) Name() string { return "example" }

func (canonProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	return core.ChatResponse{
		Content: `<analysis>chronological</analysis>
<summary>
1. Primary Request and Intent:
   User asked for a deck.

2. Key Technical Concepts:
   - none

3. Files and Artifacts:
   - none

4. Errors and Fixes:
   - none

5. Problem Solving:
   straightforward

6. All User Messages:
   - "make me a deck"

7. Pending Tasks:
   - none

8. Current Work:
   compaction example

9. Optional Next Step:
   none
</summary>`,
	}, nil
}

// ExampleNewStructuredCompactor shows the typical 3-line wiring for
// running a structured compaction call against a Provider.
func ExampleNewStructuredCompactor() {
	c := compaction.NewStructuredCompactor(canonProvider{})

	result, _ := c.Compact(context.Background(), core.CompactRequest{
		Messages: []core.ChatMessage{
			{Role: "user", Content: "make me a deck"},
		},
	})

	fmt.Println(len(result.Sections) >= 9)
	// Output: true
}
