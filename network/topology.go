// network/topology.go
//
// Read-only snapshot of a Network's graph: which children are registered,
// which supervisor policies are applied, and (in future) what kind of
// agent each child is.
package network

import (
	"fmt"
	"strings"

	"github.com/nevindra/oasis/core"
)

// Topology is a read-only snapshot of a Network's graph. Returned by
// Network.Topology(). The snapshot reflects state at call time; subsequent
// AddAgent/RemoveAgent does not mutate a previously returned Topology.
type Topology struct {
	Root  string // the Network's Name
	Nodes []Node // one per child agent, sorted by name
	Edges []Edge // router -> child relationships
}

// Node is one agent in the topology.
type Node struct {
	Name        string
	Description string
	Kind        NodeKind
	Supervisors []SupervisorSummary
}

// NodeKind identifies what kind of Agent a node is. Determined by type
// assertion on the underlying Agent.
type NodeKind string

const (
	KindLLMAgent NodeKind = "llm-agent"
	KindNetwork  NodeKind = "network"
	KindUnknown  NodeKind = "unknown"
)

// Edge represents a routing relationship from the Network's router to a
// child. Today every child has exactly one edge from the root.
type Edge struct {
	From string
	To   string
}

// SupervisorSummary is a human-readable label for an applied policy.
// E.g. {Kind: "restart", Params: {"max": "3"}}.
type SupervisorSummary struct {
	Kind   string
	Params map[string]string
}

// Topology returns a read-only snapshot of the Network's graph.
func (n *Network) Topology() Topology {
	n.mu.RLock()
	defer n.mu.RUnlock()
	top := Topology{Root: n.Name()}
	for _, name := range n.sortedAgentNames {
		child := n.agents[name]
		var perChild SupervisorPolicy
		if n.supervisorPerChild != nil {
			perChild = n.supervisorPerChild[name]
		}
		top.Nodes = append(top.Nodes, Node{
			Name:        name,
			Description: child.Description(),
			Kind:        classifyAgent(child),
			Supervisors: summarizeSupervisors(n.supervisor, perChild),
		})
		top.Edges = append(top.Edges, Edge{From: top.Root, To: name})
	}
	return top
}

// classifyAgent uses type assertion to detect Network children. Walks through
// supervisor wrappers (anything with Unwrap() core.Agent) so a Network wrapped
// in restart/fallback/breaker is still classified as KindNetwork. Anything
// else falls back to KindLLMAgent.
func classifyAgent(a core.Agent) NodeKind {
	for a != nil {
		if _, ok := a.(*Network); ok {
			return KindNetwork
		}
		u, ok := a.(interface{ Unwrap() core.Agent })
		if !ok {
			break
		}
		inner := u.Unwrap()
		if inner == nil || inner == a {
			break
		}
		a = inner
	}
	return KindLLMAgent
}

func summarizeSupervisors(networkWide SupervisorPolicy, perChild SupervisorPolicy) []SupervisorSummary {
	var out []SupervisorSummary
	if networkWide != nil {
		out = append(out, summarize(networkWide))
	}
	if perChild != nil {
		out = append(out, summarize(perChild))
	}
	return out
}

// summarize converts a SupervisorPolicy into a SupervisorSummary using a
// type switch over the built-in policies. Custom policies return
// SupervisorSummary{Kind: "custom"}.
func summarize(p SupervisorPolicy) SupervisorSummary {
	switch v := p.(type) {
	case *restartPolicy:
		return SupervisorSummary{Kind: "restart", Params: map[string]string{"max": fmt.Sprint(v.max)}}
	case *fallbackPolicy:
		return SupervisorSummary{Kind: "fallback", Params: map[string]string{"backup": v.backup.Name()}}
	case *quorumPolicy:
		return SupervisorSummary{Kind: "quorum", Params: map[string]string{"ask": fmt.Sprint(v.askN), "threshold": fmt.Sprint(v.threshold)}}
	case *breakerPolicy:
		return SupervisorSummary{Kind: "circuit-breaker", Params: map[string]string{"threshold": fmt.Sprint(v.threshold), "cooldown": v.cooldown.String()}}
	case *chainPolicy:
		parts := make([]string, len(v.policies))
		for i, sub := range v.policies {
			parts[i] = summarize(sub).Kind
		}
		return SupervisorSummary{Kind: "chain", Params: map[string]string{"policies": strings.Join(parts, ",")}}
	default:
		return SupervisorSummary{Kind: "custom"}
	}
}
