package mcp

import (
	"net/http"
	"os"
	"testing"
)

func TestBearerAuth_LiteralToken(t *testing.T) {
	a := BearerAuth{Token: "abc123"}
	req, _ := http.NewRequest("GET", "http://x", nil)
	if err := a.Apply(req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer abc123" {
		t.Errorf("got %q", got)
	}
}

func TestBearerAuth_EnvVar(t *testing.T) {
	t.Setenv("TEST_TOKEN", "env-token")
	a := BearerAuth{EnvVar: "TEST_TOKEN"}
	req, _ := http.NewRequest("GET", "http://x", nil)
	if err := a.Apply(req); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer env-token" {
		t.Errorf("got %q", got)
	}
}

func TestBearerAuth_MissingEnvVar(t *testing.T) {
	os.Unsetenv("MISSING_TOKEN_VAR")
	a := BearerAuth{EnvVar: "MISSING_TOKEN_VAR"}
	req, _ := http.NewRequest("GET", "http://x", nil)
	err := a.Apply(req)
	if err == nil {
		t.Errorf("expected error for missing env var")
	}
}

func TestBearerAuth_BothLiteralAndEnvVar(t *testing.T) {
	// Literal takes precedence over EnvVar (deterministic, no surprise).
	t.Setenv("TEST_TOKEN", "env-token")
	a := BearerAuth{Token: "literal", EnvVar: "TEST_TOKEN"}
	req, _ := http.NewRequest("GET", "http://x", nil)
	a.Apply(req)
	if got := req.Header.Get("Authorization"); got != "Bearer literal" {
		t.Errorf("got %q", got)
	}
}
