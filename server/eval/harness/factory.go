package harness

import (
	"fmt"
	"net/http"

	"cursortab/engine"
	"cursortab/provider"
	_ "cursortab/provider/all"
	"cursortab/types"
)

// BuildProviderForTarget constructs a provider for the target's pinned
// dialect, wired with the given HTTP transport for cassette record/replay.
// baseCfg is merged on top of the target's own Model/URL overrides.
func BuildProviderForTarget(t Target, baseCfg *types.ProviderConfig, transport http.RoundTripper) (engine.Provider, error) {
	if t.Dialect == "" {
		return nil, fmt.Errorf("harness: target %q has an empty dialect. The harness always pins one so prompts stay deterministic", t.Name)
	}
	cfg := mergeConfig(baseCfg, t)
	applyRoleBudgets(t, cfg)

	prov, err := provider.Build(t.Role, t.Dialect, cfg)
	if err != nil {
		return nil, fmt.Errorf("harness: target %q: %w", t.Name, err)
	}
	if transport != nil {
		setter, ok := prov.(interface{ SetHTTPTransport(http.RoundTripper) })
		if !ok {
			return nil, fmt.Errorf("harness: target %q: dialect %s does not support a transport override", t.Name, t.Dialect)
		}
		setter.SetHTTPTransport(transport)
	}
	return prov, nil
}

// applyRoleBudgets keeps eval runs deterministic: a small fixed context and
// bounded FIM generations, the same budget the dialect presets expect.
// FIM token layout itself comes from the pinned dialect, not model names.
func applyRoleBudgets(t Target, cfg *types.ProviderConfig) {
	if t.Role != provider.RoleType {
		return
	}
	if cfg.ContextSize == 0 {
		cfg.ContextSize = 1024
	}
	if cfg.Endpoint.MaxTokens == 0 || cfg.Endpoint.MaxTokens > 128 {
		cfg.Endpoint.MaxTokens = 128
	}
}

// mergeConfig applies target-level overrides on top of baseCfg.
func mergeConfig(base *types.ProviderConfig, t Target) *types.ProviderConfig {
	out := &types.ProviderConfig{}
	if base != nil {
		*out = *base
	}
	if t.URL != "" {
		out.Endpoint.URL = t.URL
	}
	if t.Model != "" {
		out.Endpoint.Model = t.Model
	}
	if out.CompletionPath == "" {
		out.CompletionPath = "/v1/completions"
	}
	if out.Endpoint.MaxTokens == 0 {
		out.Endpoint.MaxTokens = 2048
	}
	if out.Endpoint.TimeoutMs == 0 {
		out.Endpoint.TimeoutMs = 30_000
	}
	if out.Endpoint.APIKey == "" {
		out.Endpoint.APIKey = "eval-placeholder"
	}
	return out
}
