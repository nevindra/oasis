package agent

import "strings"

// buildRoutingSummary builds a JSON summary of agent and tool routing
// without json.Marshal or map allocation. Tool/agent names are always
// safe identifiers (alphanumeric + underscore), so no escaping needed.
func buildRoutingSummary(agents, tools []string) string {
	var sb strings.Builder
	sb.WriteString(`{"agents":[`)
	for i, a := range agents {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('"')
		sb.WriteString(a)
		sb.WriteByte('"')
	}
	sb.WriteString(`],"tools":[`)
	for i, t := range tools {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('"')
		sb.WriteString(t)
		sb.WriteByte('"')
	}
	sb.WriteString(`]}`)
	return sb.String()
}
