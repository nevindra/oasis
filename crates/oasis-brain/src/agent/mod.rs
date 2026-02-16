use std::collections::{HashMap, VecDeque};
use std::sync::Mutex;

use oasis_core::types::now_unix;
use tokio::sync::mpsc;

/// Status of a running sub-agent.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AgentStatus {
    Running,
    WaitingForInput,
    Done,
}

impl std::fmt::Display for AgentStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Running => write!(f, "running"),
            Self::WaitingForInput => write!(f, "waiting for input"),
            Self::Done => write!(f, "done"),
        }
    }
}

/// Handle to a spawned sub-agent task.
pub struct AgentHandle {
    pub id: String,
    pub chat_id: i64,
    pub status: Mutex<AgentStatus>,
    pub description: String,
    pub input_tx: mpsc::UnboundedSender<String>,
    pub ack_message_id: i64,
    pub original_message_id: i64,
    pub created_at: u64,
}

/// An action waiting in the queue for a slot to open.
pub struct QueuedAction {
    pub chat_id: i64,
    pub text: String,
    pub conversation_id: String,
    pub original_message_id: i64,
}

/// Manages active sub-agents, routes user replies, and queues overflow.
pub struct AgentManager {
    agents: Mutex<HashMap<String, AgentHandle>>,
    /// Maps bot message_id (from ask_user questions) → agent_id.
    message_map: Mutex<HashMap<i64, String>>,
    queue: Mutex<VecDeque<QueuedAction>>,
    max_concurrent: usize,
}

impl AgentManager {
    pub fn new(max_concurrent: usize) -> Self {
        Self {
            agents: Mutex::new(HashMap::new()),
            message_map: Mutex::new(HashMap::new()),
            queue: Mutex::new(VecDeque::new()),
            max_concurrent,
        }
    }

    /// Check if there's a free slot for a new agent.
    pub fn slots_available(&self) -> bool {
        let agents = self.agents.lock().unwrap();
        let active = agents
            .values()
            .filter(|h| {
                let s = h.status.lock().unwrap();
                matches!(*s, AgentStatus::Running | AgentStatus::WaitingForInput)
            })
            .count();
        active < self.max_concurrent
    }

    /// Register a new agent handle.
    pub fn register(&self, handle: AgentHandle) {
        let id = handle.id.clone();
        self.agents.lock().unwrap().insert(id, handle);
    }

    /// Map a bot message_id (from an ask_user question) to an agent_id.
    pub fn register_message(&self, bot_msg_id: i64, agent_id: String) {
        self.message_map.lock().unwrap().insert(bot_msg_id, agent_id);
    }

    /// Try to route a user reply to a waiting agent.
    /// Returns true if the reply was routed (caller should not process further).
    pub fn route_reply(&self, reply_to_msg_id: i64, text: &str) -> bool {
        let agent_id = {
            let map = self.message_map.lock().unwrap();
            match map.get(&reply_to_msg_id) {
                Some(id) => id.clone(),
                None => return false,
            }
        };

        let agents = self.agents.lock().unwrap();
        if let Some(handle) = agents.get(&agent_id) {
            let _ = handle.input_tx.send(text.to_string());
            return true;
        }

        false
    }

    /// Set an agent's status.
    pub fn set_status(&self, agent_id: &str, status: AgentStatus) {
        let agents = self.agents.lock().unwrap();
        if let Some(handle) = agents.get(agent_id) {
            *handle.status.lock().unwrap() = status;
        }
    }

    /// Remove an agent and its message_map entries.
    pub fn remove(&self, agent_id: &str) {
        self.agents.lock().unwrap().remove(agent_id);
        self.message_map
            .lock()
            .unwrap()
            .retain(|_, v| v != agent_id);
    }

    /// List active agents: (id, description, status, elapsed_secs).
    pub fn list_active(&self) -> Vec<(String, String, AgentStatus, u64)> {
        let now = now_unix() as u64;
        let agents = self.agents.lock().unwrap();
        agents
            .values()
            .filter_map(|h| {
                let status = *h.status.lock().unwrap();
                if status == AgentStatus::Done {
                    return None;
                }
                Some((
                    h.id.clone(),
                    h.description.clone(),
                    status,
                    now.saturating_sub(h.created_at),
                ))
            })
            .collect()
    }

    /// Add an action to the queue.
    pub fn enqueue(&self, action: QueuedAction) {
        self.queue.lock().unwrap().push_back(action);
    }

    /// Try to dequeue the next action if a slot is available.
    pub fn try_dequeue(&self) -> Option<QueuedAction> {
        if !self.slots_available() {
            return None;
        }
        self.queue.lock().unwrap().pop_front()
    }
}
