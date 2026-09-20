package harness

import (
	"fmt"
	"net/http"
	"strings"

	"cursortab/engine"
	"cursortab/eval/cassette"
	"cursortab/provider"
	_ "cursortab/provider/all"
	"cursortab/provider/copilot"
	"cursortab/provider/windsurf"
	"cursortab/types"
)

// BuildProviderForTarget constructs a real provider for the given target,
// wired with the given HTTP transport. baseCfg is merged on top of the
// target's own Model/URL overrides.
//
// cs is only used by type=copilot (LSP replay). HTTP providers ignore it.
// For copilot, if cs is non-nil, a new cassetteCopilotLSP is created;
// otherwise the caller should pass in a pre-created one via the copilotLSP param.
func BuildProviderForTarget(t Target, baseCfg *types.ProviderConfig, transport http.RoundTripper, cs *cassette.Cassette, copilotLSP *cassetteCopilotLSP) (engine.Provider, error) {
	if t.Type == "" {
		return nil, fmt.Errorf("harness: target %q has empty type", t.Name)
	}
	cfg := mergeConfig(baseCfg, t)

	// Providers that need more than config are built here. Everything else
	// goes through the shared registry so there is one place a provider is
	// constructed, not one per consumer.
	switch t.Type {
	case "mercuryapi":
		if t.URL != "" {
			return nil, fmt.Errorf("harness: target %q has URL override but mercuryapi only supports the hosted endpoint", t.Name)
		}
	case "copilot":
		if cs == nil {
			return nil, fmt.Errorf("harness: target %q (copilot) requires a cassette; copilot cannot be recorded from the standalone harness", t.Name)
		}
		if copilotLSP == nil {
			copilotLSP = newCassetteCopilotLSP(cs)
		}
		return copilot.NewProvider(copilotLSP), nil
	case "windsurf":
		p := windsurf.NewProvider(newCassetteWindsurfInfo())
		p.SetHTTPTransport(transport)
		return p, nil
	case "fim":
		applyFIMDefaults(cfg)
	}

	prov, err := provider.Build(t.Type, cfg)
	if err != nil {
		return nil, fmt.Errorf("harness: target %q: %w", t.Name, err)
	}
	provider.SetTransport(prov, transport)
	return prov, nil
}

// applyFIMDefaults fills in the token layout and generation budget the eval
// runs rely on, keyed off the model name.
func applyFIMDefaults(cfg *types.ProviderConfig) {
	if cfg.ProviderContextSize == 0 {
		cfg.ProviderContextSize = 1024
	}
	if cfg.ProviderMaxTokens == 0 || cfg.ProviderMaxTokens > 128 {
		cfg.ProviderMaxTokens = 128
	}
	// Qwen models (and Zeta, which is Qwen-based) use the standard FIM
	// tokens; inject them when targets haven't configured FIMTokens. Without
	// this, eval FIM falls back to prompt+suffix mode and Qwen completions
	// regress.
	isQwen := strings.Contains(strings.ToLower(cfg.ProviderModel), "qwen")
	if cfg.FIMTokens == nil && isQwen {
		cfg.FIMTokens = &types.FIMTokenConfig{
			Prefix: "<|fim_prefix|>",
			Suffix: "<|fim_suffix|>",
			Middle: "<|fim_middle|>",
		}
	}
	// Qwen also supports repo-level cross-file context.
	if cfg.FIMTokens != nil && isQwen {
		if cfg.FIMTokens.RepoName == "" {
			cfg.FIMTokens.RepoName = "<|repo_name|>"
		}
		if cfg.FIMTokens.FileSep == "" {
			cfg.FIMTokens.FileSep = "<|file_sep|>"
		}
	}
	// Mellum uses suffix-first token order and filename-tagged cross-file
	// context (JetBrains card format).
	isMellum := strings.Contains(strings.ToLower(cfg.ProviderModel), "mellum")
	if cfg.FIMTokens == nil && isMellum {
		cfg.FIMTokens = &types.FIMTokenConfig{
			Prefix:      "<fim_prefix>",
			Suffix:      "<fim_suffix>",
			Middle:      "<fim_middle>",
			Filename:    "<filename>",
			SuffixFirst: true,
		}
	}
}

// mergeConfig applies target-level overrides on top of baseCfg.
func mergeConfig(base *types.ProviderConfig, t Target) *types.ProviderConfig {
	out := &types.ProviderConfig{}
	if base != nil {
		*out = *base
	}
	if t.URL != "" {
		out.ProviderURL = t.URL
	}
	if t.Model != "" {
		out.ProviderModel = t.Model
	}
	if out.CompletionPath == "" {
		out.CompletionPath = "/v1/completions"
	}
	if out.ProviderMaxTokens == 0 {
		out.ProviderMaxTokens = 2048
	}
	if out.CompletionTimeout == 0 {
		out.CompletionTimeout = 30_000
	}
	if out.APIKey == "" {
		out.APIKey = "eval-placeholder"
	}
	return out
}
