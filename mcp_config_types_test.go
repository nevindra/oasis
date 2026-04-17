package oasis

import "testing"

func TestStdioMCPConfig_ImplementsInterface(t *testing.T) {
	var _ MCPServerConfig = StdioMCPConfig{Name: "x"}
}

func TestHTTPMCPConfig_ImplementsInterface(t *testing.T) {
	var _ MCPServerConfig = HTTPMCPConfig{Name: "x"}
}

func TestMCPServerName(t *testing.T) {
	s := StdioMCPConfig{Name: "stdio-x"}
	h := HTTPMCPConfig{Name: "http-x"}
	if s.mcpServerName() != "stdio-x" {
		t.Errorf("stdio: %s", s.mcpServerName())
	}
	if h.mcpServerName() != "http-x" {
		t.Errorf("http:  %s", h.mcpServerName())
	}
}

func TestBearerAuth_ImplementsAuth(t *testing.T) {
	var _ Auth = BearerAuth{Token: "x"}
}
