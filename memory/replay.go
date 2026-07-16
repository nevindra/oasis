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
	// pendingText carries a StepTypeText narration segment onto the NEXT
	// tool-call message as its Content — reproducing the wire shape the model
	// actually produced (one assistant message with content + tool_calls).
	// Replaying narration as standalone assistant messages instead teaches
	// the model that bare text mid-task is normal, and a generated bare-text
	// message terminates the loop — narrate-then-stall regressions.
	pendingText := ""
	for i, st := range steps {
		if st.Type == core.StepTypeText {
			// Mid-turn narration recorded by the loop. Verbatim turns replay
			// the full text; older turns replay the ≤500-char display digest
			// so a chatty model's narration can't accumulate unboundedly in
			// long threads (narration is invisible to the row-content trim).
			txt := st.Output
			if txt == "" {
				txt = truncateStr(st.RawOutput, 500)
			}
			if verbatim && st.RawOutput != "" {
				txt = st.RawOutput
			}
			if strings.TrimSpace(txt) != "" {
				// Concatenate rather than overwrite: an inject-continue text
				// step can be directly followed by the next iteration's
				// narration step (both precede the same tool call).
				if pendingText != "" {
					pendingText += "\n\n" + txt
				} else {
					pendingText = txt
				}
			}
			continue
		}
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
				Content:   pendingText,
				ToolCalls: []core.ToolCall{{ID: callID, Name: name, Args: args}},
			},
			core.ToolResultMessage(callID, output),
		)
		pendingText = ""
	}
	// Close with the turn's final text. A trailing narration segment with no
	// following tool call (hook-stopped turns) is the same visible text as
	// the row content — prefer the row content and drop the duplicate.
	if strings.TrimSpace(msg.Content) != "" {
		out = append(out, core.ChatMessage{Role: msg.Role, Content: msg.Content})
	} else if strings.TrimSpace(pendingText) != "" {
		out = append(out, core.ChatMessage{Role: core.RoleAssistant, Content: pendingText})
	}
	if len(out) == 0 {
		// Steps existed but none were replayable and the turn had no text;
		// keep the row visible rather than silently dropping the turn.
		return []core.ChatMessage{{Role: msg.Role, Content: msg.Content}}
	}
	return out
}

// turnReplayCost estimates the replay size of an assistant row's full tool
// outputs, in runes-as-bytes (len of the raw strings — close enough for a
// budget knob). Excluded: text steps (they replay small digests on old turns
// anyway) and protected tools (they replay full REGARDLESS of the window, so
// charging them would shrink the window without saving anything).
func turnReplayCost(msg core.Message, protected map[string]bool) int {
	cost := 0
	for _, st := range decodeSteps(msg) {
		if st.Type != core.StepTypeTool && st.Type != core.StepTypeAgent {
			continue
		}
		if protected[st.Name] || (st.Type == core.StepTypeAgent && protected["agent_"+st.Name]) {
			continue
		}
		if st.RawOutput != "" {
			cost += len(st.RawOutput)
		} else {
			cost += len(st.Output)
		}
	}
	return cost
}

// expandHistory applies expandHistoryMessage across the loaded history.
//
// Verbatim window: walking newest→oldest, assistant turns replay their full
// tool outputs while the verbatimBudget (chars of raw output) lasts; the most
// recent verbatimTurns are a floor and always replay verbatim regardless of
// size. The window is contiguous — once a turn doesn't fit, every older turn
// falls back to display digests (≤500 chars per step; protected tools
// excepted) so long threads don't drag every historical payload forever, but
// without the old hard cliff two turns back.
func expandHistory(history []core.Message, verbatimTurns, verbatimBudget int, protected []string) []core.ChatMessage {
	protectedSet := make(map[string]bool, len(protected))
	for _, p := range protected {
		protectedSet[p] = true
	}
	verbatimFrom := len(history) // index from which assistant rows are verbatim
	floor := verbatimTurns
	budget := verbatimBudget
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != core.RoleAssistant {
			continue
		}
		cost := turnReplayCost(history[i], protectedSet)
		if floor > 0 {
			// Floor turns always replay verbatim; they still consume budget
			// so a huge recent turn doesn't extend the window further back.
			verbatimFrom = i
			floor--
			budget -= cost
			continue
		}
		if budget <= 0 || cost > budget {
			break
		}
		verbatimFrom = i
		budget -= cost
	}
	out := make([]core.ChatMessage, 0, len(history))
	for i, msg := range history {
		out = append(out, expandHistoryMessage(msg, i, i >= verbatimFrom, protectedSet)...)
	}
	return out
}
