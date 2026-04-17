package oasis

import "testing"

func TestMCPServerState_String(t *testing.T) {
	cases := []struct {
		s    MCPServerState
		want string
	}{
		{MCPStateConnecting, "connecting"},
		{MCPStateHealthy, "healthy"},
		{MCPStateReconnecting, "reconnecting"},
		{MCPStateDead, "dead"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("%d: got %q want %q", c.s, got, c.want)
		}
	}
}

func TestNoopMCPLifecycle_NoCrash(t *testing.T) {
	var h MCPLifecycleHandler = NoopMCPLifecycle{}
	h.OnConnect("x", MCPServerInfo{})
	h.OnDisconnect("x", nil)
	h.OnToolCall("x", "y", nil)
	h.OnToolResult("x", "y", nil, nil)
}
