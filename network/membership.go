// network/membership.go
package network

import (
	"fmt"
	"sort"

	"github.com/nevindra/oasis/core"
)

// AddAgent registers a child agent at runtime. Thread-safe; subsequent
// dispatch sees the new agent immediately. Returns an error if the name
// conflicts with an existing child. The new child is wrapped with the
// Network's supervisor policies (network-wide + per-child) before storing.
func (n *Network) AddAgent(child core.Agent) error {
	name := child.Name()
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.agents[name]; exists {
		return fmt.Errorf("network: agent %q already exists", name)
	}
	n.agents[name] = n.wrapChild(child)
	n.sortedAgentNames = append(n.sortedAgentNames, name)
	sort.Strings(n.sortedAgentNames)
	return nil
}

// RemoveAgent removes the child with the given name. Thread-safe. Returns an
// error if no such child exists. In-flight calls to the removed child are not
// interrupted.
func (n *Network) RemoveAgent(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.agents[name]; !exists {
		return fmt.Errorf("network: agent %q not found", name)
	}
	delete(n.agents, name)
	for i, candidate := range n.sortedAgentNames {
		if candidate == name {
			n.sortedAgentNames = append(n.sortedAgentNames[:i], n.sortedAgentNames[i+1:]...)
			break
		}
	}
	return nil
}
