package openaicompat

import (
	"log/slog"
	"net/http"
)

// ProviderOption configures a Provider instance.
type ProviderOption func(*Provider)

// WithName sets the provider name returned by Name() (default "openai").
// Use this to distinguish providers in logs and observability.
func WithName(name string) ProviderOption {
	return func(p *Provider) { p.name = name }
}

// WithHTTPClient sets a custom HTTP client (e.g. for timeouts or proxies).
func WithHTTPClient(c *http.Client) ProviderOption {
	return func(p *Provider) { p.client = c }
}

// WithOptions appends request-level options (temperature, top_p, etc.)
// that are applied to every request made by this provider.
func WithOptions(opts ...Option) ProviderOption {
	return func(p *Provider) { p.opts = append(p.opts, opts...) }
}

// WithLogger sets a structured logger for the provider.
// When set, the provider emits warnings for unsupported GenerationParams fields
// (e.g. TopK). If not set, no warnings are emitted.
func WithLogger(l *slog.Logger) ProviderOption {
	return func(p *Provider) { p.logger = l }
}
