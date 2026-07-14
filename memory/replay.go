// memory/replay.go
package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis/core"
)

// prunedToolOutputPlaceholder replaces a replayed tool output whose full
// content was withheld and no display digest survived. The model still sees
// that the call happened (name + args), just not its body.
const prunedToolOutputPlaceholder = "[Old tool result content cleared]"

// stepsMetadata is the shape PersistMessages writes into the assistant
// message's Metadata column: {"steps": [...]}.
type stepsMetadata struct {
	Steps []core.StepTrace `json:"steps"`
}

// decodeSteps extracts the persisted step traces from an assistant history
// message's metadata. Returns nil when there are none (plain-text turn,
// foreign metadata, or malformed JSON — replay is best-effort).
func decodeSteps(msg core.Message) []core.StepTrace {
	if msg.Role != core.RoleAssistant || len(msg.Metadata) == 0 {
		return nil
	}
	var meta stepsMetadata
	if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
		return nil
	}
	return meta.Steps
}

// expandHistoryMessage converts one stored history message into the chat
// messages replayed to the provider. Plain messages pass through unchanged.
// An assistant message that carries persisted step traces is expanded into
// its tool exchange — for each step, an assistant message holding the tool
// call followed by the paired tool-result message — and closes with the
// turn's final assistant text (when non-empty). Every emitted tool call has
// a synthesized ID paired with its result, so provider-side
// tool_use/tool_result pairing is always consistent, and expansion happens
// per whole stored message, so history trimming can never split a pair.
//
// verbatim selects the output fidelity for non-protected tools: recent turns
// replay the full RawOutput, older turns replay the bounded display digest
// (StepTrace.Output, ≤500 chars) so long threads don't drag every historical
// tool payload forever. Tools listed in protected always replay in full
// regardless of age — the analog of opencode's PRUNE_PROTECTED_TOOLS, used
// for skill activation whose instructions must survive the whole thread.
func expandHistoryMessage(msg core.Message, seq int, verbatim bool, protected map[string]bool) []core.ChatMessage {
	steps := decodeSteps(msg)
	if len(steps) == 0 {
		return []core.ChatMessage{{Role: msg.Role, Content: msg.Content}}
	}
	out := make([]core.ChatMessage, 0, len(steps)*2+1)
	for i, st := range steps {
		if st.Type != core.StepTypeTool && st.Type != core.StepTypeAgent {
			continue
		}
		name := st.Name
		if st.Type == core.StepTypeAgent {
			// StepTrace strips the "agent_" prefix for display; restore it so
			// the replayed call matches the delegation tool the router exposes.
			name = "agent_" + name
		}
		callID := fmt.Sprintf("hist_%d_%d", seq, i)
		args := st.RawArgs
		if len(args) == 0 {
			// No raw args survived (externally built trace) — wrap the display
			// input so the args are still valid JSON.
			args, _ = json.Marshal(map[string]string{"input": st.Input})
		}
		output := st.RawOutput
		if !verbatim && !protected[name] && !protected[st.Name] {
			output = st.Output
		}
		if strings.TrimSpace(output) == "" {
			output = prunedToolOutputPlaceholder
		}
		out = append(out,
			core.ChatMessage{
				Role:      core.RoleAssistant,
				ToolCalls: []core.ToolCall{{ID: callID, Name: name, Args: args}},
			},
			core.ToolResultMessage(callID, output),
		)
	}
	if strings.TrimSpace(msg.Content) != "" {
		out = append(out, core.ChatMessage{Role: msg.Role, Content: msg.Content})
	}
	if len(out) == 0 {
		// Steps existed but none were replayable and the turn had no text;
		// keep the row visible rather than silently dropping the turn.
		return []core.ChatMessage{{Role: msg.Role, Content: msg.Content}}
	}
	return out
}

// expandHistory applies expandHistoryMessage across the loaded history.
// The most recent verbatimTurns assistant turns replay full tool outputs;
// older turns fall back to display digests (protected tools excepted).
func expandHistory(history []core.Message, verbatimTurns int, protected []string) []core.ChatMessage {
	protectedSet := make(map[string]bool, len(protected))
	for _, p := range protected {
		protectedSet[p] = true
	}
	// Mark the last verbatimTurns assistant rows as verbatim.
	verbatimFrom := len(history) // index from which assistant rows are verbatim
	if verbatimTurns > 0 {
		remaining := verbatimTurns
		for i := len(history) - 1; i >= 0 && remaining > 0; i-- {
			if history[i].Role == core.RoleAssistant {
				verbatimFrom = i
				remaining--
			}
		}
	}
	out := make([]core.ChatMessage, 0, len(history))
	for i, msg := range history {
		out = append(out, expandHistoryMessage(msg, i, i >= verbatimFrom, protectedSet)...)
	}
	return out
}
