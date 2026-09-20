package provider

import (
	"fmt"
	"net/http"
	"sort"

	"cursortab/engine"
	"cursortab/types"
)

// Factory builds a provider from a fully resolved config. Providers that need
// more than config (copilot and windsurf need the editor buffer or a live LSP
// shim) are not registered and are constructed by their callers.
type Factory func(*types.ProviderConfig) engine.Provider

var registry = map[string]Factory{}

// Register makes a provider type available to Build. Leaf providers call this
// from init so one registry serves every consumer instead of a switch per
// caller. Registering the same name twice panics: that is a programming error,
// not a runtime condition.
func Register(name string, factory Factory) {
	if _, exists := registry[name]; exists {
		panic("provider: duplicate registration for " + name)
	}
	registry[name] = factory
}

// Build constructs the provider registered under name, which is a
// types.ProviderType value.
func Build(name string, config *types.ProviderConfig) (engine.Provider, error) {
	factory, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unsupported provider type: %s", name)
	}
	return factory(config), nil
}

// Names lists the config-buildable provider names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TransportSetter is implemented by providers whose HTTP client can be
// swapped. Used by the eval harness for cassette record and replay.
type TransportSetter interface {
	SetHTTPTransport(http.RoundTripper)
}

// SetTransport installs a transport override on a provider that supports one.
// Nil transports are ignored, so callers can pass an optional override.
func SetTransport(p engine.Provider, transport http.RoundTripper) {
	if transport == nil {
		return
	}
	if setter, ok := p.(TransportSetter); ok {
		setter.SetHTTPTransport(transport)
	}
}
