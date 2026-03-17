// Package catalog provides dynamic model discovery across LLM providers.
//
// ModelCatalog merges static registry data (compiled in) with live provider
// API calls to give a complete picture: pricing and capabilities from static
// data, availability from live APIs.
//
// End users add providers at runtime (pick platform, enter API key, browse
// models, select). Developers wire up the catalog once — no hardcoded
// provider names.
//
//	cat := catalog.NewModelCatalog()
//	cat.Add("qwen", apiKey)
//	models, _ := cat.ListProvider(ctx, "qwen")
//	llm, _ := cat.CreateProvider(ctx, "qwen/qwen-turbo")
package catalog

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/provider/gemini"
	"github.com/nevindra/oasis/provider/openaicompat"
)

// ModelCatalog discovers and caches models across multiple providers.
// Safe for concurrent use.
type ModelCatalog struct {
	mu sync.RWMutex

	platforms    []oasis.Platform // built-in + custom
	platformIdx map[string]int   // lowercase name → index in platforms

	providers map[string]*providerEntry // lowercase identifier → entry
	maxProv   int

	ttl     time.Duration
	refresh RefreshStrategy
}

// providerEntry tracks a registered provider and its cached models.
type providerEntry struct {
	platform oasis.Platform
	apiKey   string
	cached   []oasis.ModelInfo
	fetchedAt time.Time
}

// RefreshStrategy controls when live API calls are made.
type RefreshStrategy int

const (
	// RefreshNone uses static data only. No external calls for discovery.
	RefreshNone RefreshStrategy = iota
	// RefreshOnDemand makes a live API call on List/ListProvider,
	// cached with TTL. This is the default.
	RefreshOnDemand
)

// CatalogOption configures a ModelCatalog.
type CatalogOption func(*ModelCatalog)

// WithCatalogTTL sets the cache duration for live API results. Default: 1 hour.
func WithCatalogTTL(d time.Duration) CatalogOption {
	return func(c *ModelCatalog) { c.ttl = d }
}

// WithMaxProviders sets the maximum number of providers that can be registered.
// Default: 50. Prevents unbounded growth.
func WithMaxProviders(n int) CatalogOption {
	return func(c *ModelCatalog) { c.maxProv = n }
}

// WithRefresh sets the refresh strategy for model data.
// Default: RefreshOnDemand.
func WithRefresh(s RefreshStrategy) CatalogOption {
	return func(c *ModelCatalog) { c.refresh = s }
}

// NewModelCatalog creates a catalog with optional configuration.
func NewModelCatalog(opts ...CatalogOption) *ModelCatalog {
	c := &ModelCatalog{
		providers: make(map[string]*providerEntry),
		maxProv:   50,
		ttl:       1 * time.Hour,
		refresh:   RefreshOnDemand,
	}
	for _, opt := range opts {
		opt(c)
	}

	// Index built-in platforms.
	c.platforms = make([]oasis.Platform, len(builtinPlatforms))
	copy(c.platforms, builtinPlatforms)
	c.platformIdx = make(map[string]int, len(c.platforms))
	for i, p := range c.platforms {
		c.platformIdx[strings.ToLower(p.Name)] = i
	}

	return c
}

// Platforms returns all available platforms (built-in + registered custom).
func (c *ModelCatalog) Platforms() []oasis.Platform {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]oasis.Platform, len(c.platforms))
	copy(out, c.platforms)
	return out
}

// RegisterPlatform adds a custom platform definition to the catalog.
// Use for providers that Oasis doesn't ship with yet.
func (c *ModelCatalog) RegisterPlatform(p oasis.Platform) error {
	if p.Name == "" {
		return fmt.Errorf("catalog: platform name is required")
	}
	if p.BaseURL == "" {
		return fmt.Errorf("catalog: platform %q requires a BaseURL", p.Name)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	key := strings.ToLower(p.Name)
	if idx, ok := c.platformIdx[key]; ok {
		// Update existing platform.
		c.platforms[idx] = p
		return nil
	}
	c.platformIdx[key] = len(c.platforms)
	c.platforms = append(c.platforms, p)
	return nil
}

// Add registers a known platform with an API key. The platform name is
// case-insensitive ("Qwen", "qwen", "QWEN" all match).
// If the platform was already added, the credentials are updated (overwrite).
func (c *ModelCatalog) Add(platform, apiKey string) error {
	key := strings.ToLower(platform)

	c.mu.Lock()
	defer c.mu.Unlock()

	idx, ok := c.platformIdx[key]
	if !ok {
		return fmt.Errorf("catalog: unknown platform %q (use AddCustom for custom providers, or RegisterPlatform to add a new platform)", platform)
	}

	if _, exists := c.providers[key]; !exists {
		if len(c.providers) >= c.maxProv {
			return fmt.Errorf("catalog: maximum providers (%d) reached", c.maxProv)
		}
	}

	c.providers[key] = &providerEntry{
		platform: c.platforms[idx],
		apiKey:   apiKey,
	}
	return nil
}

// AddCustom registers a custom provider with a base URL and identifier.
// Use for self-hosted (Ollama, vLLM) or providers not in the built-in list.
// Defaults to OpenAI-compatible protocol. Use RegisterPlatform for
// non-OpenAI-compatible custom providers (e.g., Gemini protocol proxies).
// If the identifier was already registered, the credentials are updated (overwrite).
func (c *ModelCatalog) AddCustom(identifier, baseURL, apiKey string) error {
	if identifier == "" {
		return fmt.Errorf("catalog: identifier is required")
	}
	if baseURL == "" {
		return fmt.Errorf("catalog: baseURL is required for custom provider %q", identifier)
	}

	key := strings.ToLower(identifier)

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.providers[key]; !exists {
		if len(c.providers) >= c.maxProv {
			return fmt.Errorf("catalog: maximum providers (%d) reached", c.maxProv)
		}
	}

	c.providers[key] = &providerEntry{
		platform: oasis.Platform{
			Name:     identifier,
			Protocol: oasis.ProtocolOpenAICompat,
			BaseURL:  baseURL,
		},
		apiKey: apiKey,
	}
	return nil
}

// Remove removes a provider by identifier.
func (c *ModelCatalog) Remove(identifier string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.providers, strings.ToLower(identifier))
}

// List returns models from all registered providers, merging static metadata
// with live API results.
func (c *ModelCatalog) List(ctx context.Context) ([]oasis.ModelInfo, error) {
	c.mu.RLock()
	ids := make([]string, 0, len(c.providers))
	for id := range c.providers {
		ids = append(ids, id)
	}
	c.mu.RUnlock()

	var all []oasis.ModelInfo
	for _, id := range ids {
		models, err := c.ListProvider(ctx, id)
		if err != nil {
			// Degrade gracefully: skip providers that fail, continue with others.
			continue
		}
		all = append(all, models...)
	}
	return all, nil
}

// ListProvider returns models from a single registered provider.
// Merges static metadata with live API results (if available and refresh is enabled).
func (c *ModelCatalog) ListProvider(ctx context.Context, identifier string) ([]oasis.ModelInfo, error) {
	key := strings.ToLower(identifier)

	c.mu.RLock()
	entry, ok := c.providers[key]
	if !ok {
		c.mu.RUnlock()
		return nil, fmt.Errorf("catalog: provider %q not registered (use Add or AddCustom first)", identifier)
	}

	// Return cached if fresh.
	if entry.cached != nil && time.Since(entry.fetchedAt) < c.ttl {
		out := make([]oasis.ModelInfo, len(entry.cached))
		copy(out, entry.cached)
		c.mu.RUnlock()
		return out, nil
	}
	c.mu.RUnlock()

	// Static data for this provider.
	staticModels := staticByProvider(key)

	// Live fetch (if enabled).
	if c.refresh == RefreshNone {
		return staticModels, nil
	}

	lister := listerFor(entry.platform.Protocol, key)
	liveModels, err := lister.listModels(ctx, entry.platform.BaseURL, entry.apiKey)
	if err != nil {
		// Degrade to static-only if live fetch fails.
		if len(staticModels) > 0 {
			return staticModels, nil
		}
		return nil, err
	}

	// Merge: static metadata + live availability.
	merged := mergeModels(staticModels, liveModels)

	// Cache the result.
	c.mu.Lock()
	if e, ok := c.providers[key]; ok {
		e.cached = merged
		e.fetchedAt = time.Now()
	}
	c.mu.Unlock()

	return merged, nil
}

// Validate checks if a model exists and is not deprecated.
// modelID uses the "provider/model" format (e.g., "openai/gpt-4o").
func (c *ModelCatalog) Validate(ctx context.Context, modelID string) error {
	provider, model := oasis.ParseModelID(modelID)
	if provider == "" || model == "" {
		return fmt.Errorf("catalog: invalid model ID %q (expected \"provider/model\")", modelID)
	}

	models, err := c.ListProvider(ctx, provider)
	if err != nil {
		return err
	}

	for _, m := range models {
		if m.ID == model {
			if m.Deprecated {
				msg := fmt.Sprintf("catalog: model %q is deprecated", modelID)
				if m.DeprecationMsg != "" {
					msg += " (" + m.DeprecationMsg + ")"
				}
				return fmt.Errorf("%s", msg)
			}
			if m.Status == oasis.ModelStatusUnavailable {
				return fmt.Errorf("catalog: model %q is no longer available", modelID)
			}
			return nil
		}
	}

	return fmt.Errorf("catalog: model %q not found", modelID)
}

// CreateProvider creates a ready-to-use oasis.Provider for the given model.
// modelID uses the "provider/model" format (e.g., "openai/gpt-4o").
// Validates the model before creating the provider.
func (c *ModelCatalog) CreateProvider(ctx context.Context, modelID string) (oasis.Provider, error) {
	providerName, model := oasis.ParseModelID(modelID)
	if providerName == "" || model == "" {
		return nil, fmt.Errorf("catalog: invalid model ID %q (expected \"provider/model\")", modelID)
	}

	if err := c.Validate(ctx, modelID); err != nil {
		return nil, err
	}

	key := strings.ToLower(providerName)
	c.mu.RLock()
	entry, ok := c.providers[key]
	if !ok {
		c.mu.RUnlock()
		return nil, fmt.Errorf("catalog: provider %q not registered", providerName)
	}
	platform := entry.platform
	apiKey := entry.apiKey
	c.mu.RUnlock()

	return createProvider(platform, apiKey, model)
}

// CreateProviderByID creates a provider using separate provider and model strings.
// This is an alternative to the "provider/model" format for programmatic use.
func (c *ModelCatalog) CreateProviderByID(ctx context.Context, provider, model string) (oasis.Provider, error) {
	return c.CreateProvider(ctx, provider+"/"+model)
}

// listerFor returns the appropriate model lister for the given protocol.
func listerFor(protocol oasis.Protocol, provider string) modelLister {
	switch protocol {
	case oasis.ProtocolGemini:
		return &geminiLister{}
	default:
		return &openaiLister{provider: provider}
	}
}

// createProvider creates an oasis.Provider from platform info.
func createProvider(platform oasis.Platform, apiKey, model string) (oasis.Provider, error) {
	switch platform.Protocol {
	case oasis.ProtocolGemini:
		return gemini.New(apiKey, model), nil
	default:
		var provOpts []openaicompat.ProviderOption
		provOpts = append(provOpts, openaicompat.WithName(strings.ToLower(platform.Name)))
		return openaicompat.NewProvider(apiKey, model, platform.BaseURL, provOpts...), nil
	}
}

// mergeModels combines static metadata with live availability data.
// Static provides pricing, capabilities, context windows.
// Live provides availability status.
func mergeModels(static, live []oasis.ModelInfo) []oasis.ModelInfo {
	// Index static models by ID.
	staticIdx := make(map[string]oasis.ModelInfo, len(static))
	for _, m := range static {
		staticIdx[m.ID] = m
	}

	// Index live models by ID.
	liveIdx := make(map[string]oasis.ModelInfo, len(live))
	for _, m := range live {
		liveIdx[m.ID] = m
	}

	seen := make(map[string]bool, len(static)+len(live))
	out := make([]oasis.ModelInfo, 0, len(static)+len(live))

	// Models in live data: merge with static metadata if available.
	for _, lm := range live {
		seen[lm.ID] = true
		if sm, ok := staticIdx[lm.ID]; ok {
			out = append(out, enrichLiveWithStatic(lm, sm))
		} else {
			// New model not in static — live only, minimal metadata.
			out = append(out, lm)
		}
	}

	// Models only in static: mark as unavailable.
	for _, sm := range static {
		if seen[sm.ID] {
			continue
		}
		sm.Status = oasis.ModelStatusUnavailable
		out = append(out, sm)
	}

	return out
}

// enrichLiveWithStatic fills in metadata gaps in live data from static data.
func enrichLiveWithStatic(live, static oasis.ModelInfo) oasis.ModelInfo {
	m := live
	m.Status = oasis.ModelStatusAvailable

	// Fill display name from static if live doesn't have one.
	if m.DisplayName == "" {
		m.DisplayName = static.DisplayName
	}

	// Fill context windows from static if live doesn't have them.
	if m.InputContext == 0 {
		m.InputContext = static.InputContext
	}
	if m.OutputContext == 0 {
		m.OutputContext = static.OutputContext
	}

	// Prefer static capabilities if live has none.
	if m.Capabilities == (oasis.ModelCapabilities{}) {
		m.Capabilities = static.Capabilities
	}

	// Use static pricing if live doesn't have it (most providers don't expose pricing).
	if m.Pricing == nil && static.Pricing != nil {
		m.Pricing = static.Pricing
	}

	// Static deprecation info takes precedence.
	if static.Deprecated {
		m.Deprecated = true
		m.DeprecationMsg = static.DeprecationMsg
	}

	return m
}

// staticByProvider returns static models matching the given provider identifier.
func staticByProvider(provider string) []oasis.ModelInfo {
	var out []oasis.ModelInfo
	for _, m := range staticModels {
		if strings.EqualFold(m.Provider, provider) {
			out = append(out, m)
		}
	}
	return out
}
