package mcp

import "testing"

func TestStdioMCPConfig_ImplementsInterface(t *testing.T) {
	var _ ServerConfig = StdioConfig{Name: "x"}
}

func TestHTTPMCPConfig_ImplementsInterface(t *testing.T) {
	var _ ServerConfig = HTTPConfig{Name: "x"}
}

func TestMCPServerName(t *testing.T) {
	s := StdioConfig{Name: "stdio-x"}
	h := HTTPConfig{Name: "http-x"}
	if s.serverName() != "stdio-x" {
		t.Errorf("stdio: %s", s.serverName())
	}
	if h.serverName() != "http-x" {
		t.Errorf("http:  %s", h.serverName())
	}
}

func TestBearerAuth_ImplementsAuth(t *testing.T) {
	var _ Auth = BearerAuth{Token: "x"}
}
