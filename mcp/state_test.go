package mcp

import "testing"

func TestMCPServerState_String(t *testing.T) {
	cases := []struct {
		s    ServerState
		want string
	}{
		{StateConnecting, "connecting"},
		{StateHealthy, "healthy"},
		{StateReconnecting, "reconnecting"},
		{StateDead, "dead"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("%d: got %q want %q", c.s, got, c.want)
		}
	}
}

func TestNoopMCPLifecycle_NoCrash(t *testing.T) {
	var h LifecycleHandler = NoopLifecycle{}
	h.OnConnect("x", ServerMetadata{})
	h.OnDisconnect("x", nil)
	h.OnToolCall("x", "y", nil)
	h.OnToolResult("x", "y", nil, nil)
}
