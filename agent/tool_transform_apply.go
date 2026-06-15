package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nevindra/oasis/core"
)

// redactionFailed is substituted into a human-facing sink (Display/Transcript)
// when its transform panics. Fail-closed: never leak the raw payload on error.
const redactionFailed = "[oasis: redaction failed]"

// applyResultTransform runs st.Result with panic recovery.
//   - st nil or st.Result nil: returns r unchanged (passthrough).
//   - transform panics + failClosed (Display/Transcript): returns a placeholder
//     ToolResult, dropping Content/Error/Attachments/UI so nothing raw leaks.
//   - transform panics + !failClosed (Model): returns r unchanged (fail open —
//     keep the agent functional; the model payload is not a leak).
//
// Rationale for the asymmetry: a buggy redactor must never expose secrets to a
// human-facing sink, but it also must not break the agent loop for the model.
// Do not "simplify" this into uniform fail-open.
func applyResultTransform(st *core.SinkTransform, name string, r core.ToolResult, failClosed bool, logger *slog.Logger) (out core.ToolResult) {
	if st == nil || st.Result == nil {
		return r
	}
	defer func() {
		if p := recover(); p != nil {
			if logger != nil {
				logger.Warn("tool result transform panicked",
					"tool", name, "fail_closed", failClosed, "panic", fmt.Sprintf("%v", p))
			}
			if failClosed {
				if r.Error != "" {
					out = core.ToolResult{Error: redactionFailed}
				} else {
					out = core.ToolResult{Content: redactionFailed}
				}
			} else {
				out = r
			}
		}
	}()
	return st.Result(name, r)
}

// applyArgsTransform runs st.Args with panic recovery. Args transforms only run
// for the human-facing sinks (Display/Transcript), so a panic always fails
// closed to a quoted placeholder string.
func applyArgsTransform(st *core.SinkTransform, name string, args json.RawMessage, logger *slog.Logger) (out json.RawMessage) {
	if st == nil || st.Args == nil {
		return args
	}
	defer func() {
		if p := recover(); p != nil {
			if logger != nil {
				logger.Warn("tool args transform panicked", "tool", name, "panic", fmt.Sprintf("%v", p))
			}
			out = json.RawMessage(`"` + redactionFailed + `"`)
		}
	}()
	return st.Args(name, args)
}
