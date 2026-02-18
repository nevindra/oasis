package main

import (
	"context"
	"fmt"
	"time"

	oasis "github.com/nevindra/oasis"
)

// clockPreProcessor appends the current date and time to the system message
// before each LLM call so the agent is always aware of the current time.
// tzOffset is the UTC offset in hours (e.g. 7 for WIB / UTC+7).
type clockPreProcessor struct {
	loc *time.Location
}

func newClockPreProcessor(tzOffset int) *clockPreProcessor {
	name := fmt.Sprintf("UTC%+d", tzOffset)
	return &clockPreProcessor{loc: time.FixedZone(name, tzOffset*3600)}
}

func (p *clockPreProcessor) PreLLM(_ context.Context, req *oasis.ChatRequest) error {
	now := time.Now().In(p.loc)
	line := "\n\n## Current date and time\n" + now.Format("Monday, 02 January 2006 — 15:04 MST")
	for i, msg := range req.Messages {
		if msg.Role == "system" {
			req.Messages[i].Content += line
			return nil
		}
	}
	return nil
}
