package openaicompat

import (
	"log/slog"
	"net/http"
)

// ProviderOption configures a chat Provider instance.
//
// Why: ProviderOption and EmbeddingOption are interfaces (not bare func types)
// so that the shared knobs WithName / WithHTTPClient can return a single value
// that satisfies BOTH option types. Go forbids two package-level funcs named
// WithName, so the prior code duplicated them as WithEmbeddingName /
// WithEmbeddingHTTPClient. Modeling the option as an interface lets one
// constructor cover both providers without a name collision.
type ProviderOption interface {
	applyProvider(*Provider)
}

// providerOptionFunc adapts a closure to ProviderOption (provider-only knobs).
type providerOptionFunc func(*Provider)

func (f providerOptionFunc) applyProvider(p *Provider) { f(p) }

// WithOptions appends request-level options (temperature, top_p, etc.)
// that are applied to every request made by this provider.
func WithOptions(opts ...Option) ProviderOption {
	return providerOptionFunc(func(p *Provider) { p.opts = append(p.opts, opts...) })
}

// WithLogger sets a structured logger for the provider.
// When set, the provider emits warnings for unsupported GenerationParams fields
// (e.g. TopK). If not set, no warnings are emitted.
func WithLogger(l *slog.Logger) ProviderOption {
	return providerOptionFunc(func(p *Provider) { p.logger = l })
}

// commonOption configures the knobs shared by chat and embedding providers
// (name, HTTP client). It satisfies both ProviderOption and EmbeddingOption so a
// single WithName / WithHTTPClient value can be passed to NewProvider or
// NewEmbedding.
type commonOption struct {
	name   *string
	client *http.Client
}

func (o commonOption) applyProvider(p *Provider) {
	if o.name != nil {
		p.name = *o.name
	}
	if o.client != nil {
		p.client = o.client
	}
}

func (o commonOption) applyEmbedding(e *Embedding) {
	if o.name != nil {
		e.name = *o.name
	}
	if o.client != nil {
		e.client = o.client
	}
}

// WithName sets the provider name returned by Name() (default "openai").
// Use this to distinguish providers in logs and observability. Accepted by both
// NewProvider and NewEmbedding.
func WithName(name string) commonOption {
	return commonOption{name: &name}
}

// WithHTTPClient sets a custom HTTP client (e.g. for timeouts or proxies).
// Accepted by both NewProvider and NewEmbedding.
func WithHTTPClient(c *http.Client) commonOption {
	return commonOption{client: c}
}
