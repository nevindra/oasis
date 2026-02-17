package oasis

import "testing"

func TestErrLLMError(t *testing.T) {
	tests := []struct {
		provider string
		message  string
		want     string
	}{
		{"gemini", "rate limited", "gemini: rate limited"},
		{"openai", "context length exceeded", "openai: context length exceeded"},
	}
	for _, tt := range tests {
		e := &ErrLLM{Provider: tt.provider, Message: tt.message}
		if got := e.Error(); got != tt.want {
			t.Errorf("ErrLLM{%q, %q}.Error() = %q, want %q", tt.provider, tt.message, got, tt.want)
		}
	}
}

func TestErrLLMImplementsError(t *testing.T) {
	var _ error = (*ErrLLM)(nil)
}

func TestErrHTTPError(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   string
	}{
		{429, "too many requests", "http 429: too many requests"},
		{500, "internal server error", "http 500: internal server error"},
	}
	for _, tt := range tests {
		e := &ErrHTTP{Status: tt.status, Body: tt.body}
		if got := e.Error(); got != tt.want {
			t.Errorf("ErrHTTP{%d, %q}.Error() = %q, want %q", tt.status, tt.body, got, tt.want)
		}
	}
}

func TestErrHTTPImplementsError(t *testing.T) {
	var _ error = (*ErrHTTP)(nil)
}

func TestErrLLMEmptyFields(t *testing.T) {
	e := &ErrLLM{}
	want := ": "
	if got := e.Error(); got != want {
		t.Errorf("ErrLLM{}.Error() = %q, want %q", got, want)
	}
}

func TestErrHTTPZeroStatus(t *testing.T) {
	e := &ErrHTTP{}
	want := "http 0: "
	if got := e.Error(); got != want {
		t.Errorf("ErrHTTP{}.Error() = %q, want %q", got, want)
	}
}
