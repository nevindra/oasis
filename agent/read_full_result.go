package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/nevindra/oasis/core"
)

// ReadFullResultIn is the input schema for the read_full_result built-in tool.
type ReadFullResultIn struct {
	ID     string `json:"id"     describe:"the opaque id from a truncation marker"`
	Offset int    `json:"offset" describe:"starting rune offset"`
	Length int    `json:"length" describe:"max runes to return (recommend 50000)"`
}

// ReadFullResultOut is the output schema for the read_full_result built-in tool.
type ReadFullResultOut struct {
	Content string `json:"content"`
	Total   int    `json:"total"`
	More    bool   `json:"more"`
}

type readFullResultTool struct {
	store core.ToolResultStore
}

// NewReadFullResultTool returns the read_full_result tool bound to the given
// store. The tool is auto-registered on every agent that has a ToolResultStore
// configured (which is the default).
func NewReadFullResultTool(store core.ToolResultStore) core.AnyTool {
	return core.Erase[ReadFullResultIn, ReadFullResultOut](&readFullResultTool{store: store})
}

func (t *readFullResultTool) Definition() core.ToolMeta {
	return core.ToolMeta{
		Name: "read_full_result",
		Description: "Retrieve a slice of a previously-truncated tool result. " +
			"Use the id from a [truncated at N runes of M total. Use read_full_result(...)] marker.",
	}
}

func (t *readFullResultTool) Execute(ctx context.Context, in ReadFullResultIn) (ReadFullResultOut, error) {
	if in.Length <= 0 {
		in.Length = 50_000
	}
	content, total, err := t.store.Get(ctx, in.ID, in.Offset, in.Length)
	if errors.Is(err, core.ErrToolResultNotFound) {
		return ReadFullResultOut{}, fmt.Errorf("result id %q not found or expired", in.ID)
	}
	if err != nil {
		return ReadFullResultOut{}, err
	}
	runeCount := len([]rune(content))
	more := in.Offset+runeCount < total
	out := ReadFullResultOut{
		Content: content,
		Total:   total,
		More:    more,
	}
	if more {
		nextOffset := in.Offset + runeCount
		out.Content += fmt.Sprintf(
			"\n\n[%d of %d runes returned, more remaining — call read_full_result(id=%q, offset=%d) for the next chunk]",
			nextOffset, total, in.ID, nextOffset,
		)
	}
	return out, nil
}
