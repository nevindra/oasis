package mcp

import (
	"fmt"
	"net/http"
	"os"
)

// Auth applies authentication to outgoing HTTP requests.
// Implementations: BearerAuth (v1). OAuthAuth, MTLSAuth: post-v1.
type Auth interface {
	Apply(req *http.Request) error
	isAuth()
}

// BearerAuth sets "Authorization: Bearer <token>" on the request.
// Token may be literal (discouraged — never logged but still in memory)
// or sourced from an environment variable (preferred).
// If both Token and EnvVar are set, Token (literal) wins for determinism.
type BearerAuth struct {
	Token  string
	EnvVar string
}

func (BearerAuth) isAuth() {}

func (a BearerAuth) Apply(req *http.Request) error {
	token := a.Token
	if token == "" && a.EnvVar != "" {
		token = os.Getenv(a.EnvVar)
		if token == "" {
			return fmt.Errorf("env var %q is empty or unset", a.EnvVar)
		}
	}
	if token == "" {
		return fmt.Errorf("BearerAuth: no token (Token or EnvVar must be set)")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}
