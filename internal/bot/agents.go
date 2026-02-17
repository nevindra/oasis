package bot

import (
	"fmt"
	"sync"
	"time"
)

// AgentStatus represents the state of a running action agent.
type AgentStatus int

const (
	AgentRunning AgentStatus = iota
	AgentWaitingForInput
)

func (s AgentStatus) String() string {
	switch s {
	case AgentRunning:
		return "running"
	case AgentWaitingForInput:
		return "waiting for input"
	default:
		return "unknown"
	}
}

// ActionAgent represents a running action agent with ask_user support.
type ActionAgent struct {
	ID            string
	ChatID        string
	Description   string
	Status        AgentStatus
	StartedAt     time.Time
	InputCh       chan string // ask_user replies routed here
	OriginalMsgID string     // message being replied to
	AckMsgID      string     // "processing..." placeholder message
}

// QueuedAction represents an action waiting for a slot.
type QueuedAction struct {
	ChatID         string
	Text           string
	ConversationID string
	OriginalMsgID  string
}

// AgentManager manages concurrent action agents with reply routing and overflow queue.
type AgentManager struct {
	mu            sync.Mutex
	agents        map[string]*ActionAgent
	messageMap    map[string]string // bot message ID -> agent ID (for ask_user routing)
	queue         []QueuedAction
	maxConcurrent int
}

// NewAgentManager creates a manager with the given concurrency limit.
func NewAgentManager(maxConcurrent int) *AgentManager {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	return &AgentManager{
		agents:        make(map[string]*ActionAgent),
		messageMap:    make(map[string]string),
		maxConcurrent: maxConcurrent,
	}
}

// SlotsAvailable returns true if there's room for a new agent.
func (m *AgentManager) SlotsAvailable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	active := 0
	for _, a := range m.agents {
		if a.Status == AgentRunning || a.Status == AgentWaitingForInput {
			active++
		}
	}
	return active < m.maxConcurrent
}

// Register adds a new agent.
func (m *AgentManager) Register(agent *ActionAgent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[agent.ID] = agent
}

// RegisterMessage maps a bot message ID to an agent ID for reply routing.
func (m *AgentManager) RegisterMessage(botMsgID, agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messageMap[botMsgID] = agentID
}

// RouteReply tries to route a user reply to a waiting agent.
// Returns true if the reply was routed (caller should not process further).
func (m *AgentManager) RouteReply(replyToMsgID, text string) bool {
	m.mu.Lock()
	agentID, ok := m.messageMap[replyToMsgID]
	if !ok {
		m.mu.Unlock()
		return false
	}
	agent, exists := m.agents[agentID]
	m.mu.Unlock()

	if !exists {
		return false
	}

	select {
	case agent.InputCh <- text:
		return true
	default:
		return false
	}
}

// SetStatus updates an agent's status.
func (m *AgentManager) SetStatus(agentID string, status AgentStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.agents[agentID]; ok {
		a.Status = status
	}
}

// Remove removes an agent and cleans up its message map entries.
func (m *AgentManager) Remove(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.agents, agentID)
	for msgID, aID := range m.messageMap {
		if aID == agentID {
			delete(m.messageMap, msgID)
		}
	}
}

// ListActive returns info about all running/waiting agents.
func (m *AgentManager) ListActive() []AgentInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	result := make([]AgentInfo, 0, len(m.agents))
	for _, a := range m.agents {
		result = append(result, AgentInfo{
			ID:          a.ID,
			Description: a.Description,
			Status:      a.Status,
			Elapsed:     now.Sub(a.StartedAt),
		})
	}
	return result
}

// AgentInfo is returned by ListActive.
type AgentInfo struct {
	ID          string
	Description string
	Status      AgentStatus
	Elapsed     time.Duration
}

// Enqueue adds an action to the overflow queue.
func (m *AgentManager) Enqueue(action QueuedAction) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, action)
}

// TryDequeue returns the next queued action if a slot is available.
func (m *AgentManager) TryDequeue() (QueuedAction, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	active := 0
	for _, a := range m.agents {
		if a.Status == AgentRunning || a.Status == AgentWaitingForInput {
			active++
		}
	}
	if active >= m.maxConcurrent || len(m.queue) == 0 {
		return QueuedAction{}, false
	}

	action := m.queue[0]
	m.queue = m.queue[1:]
	return action, true
}

// FormatStatus returns a formatted string for the /status command.
func (m *AgentManager) FormatStatus() string {
	active := m.ListActive()
	if len(active) == 0 {
		return "No active agents."
	}
	out := "Active agents:\n"
	for _, a := range active {
		shortID := a.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		out += fmt.Sprintf("- [%s] %s (%s, %ds)\n", shortID, a.Description, a.Status, int(a.Elapsed.Seconds()))
	}
	return out
}

