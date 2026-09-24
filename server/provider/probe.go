package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cursortab/logger"
	"cursortab/types"
)

// ModelInfo is one entry of GET /v1/models.
type ModelInfo struct {
	ID    string
	State string
}

// Resolved is what Probe learned about an endpoint. ContextSize stays 0 when
// the server never answered, FIMTokens holds one preset per model id when
// detectable.
type Resolved struct {
	Models      []ModelInfo
	ContextSize int
	FIMTokens   map[string]*types.FIMTokenConfig
}

const (
	probeTimeout = 3 * time.Second
	// fallbackContextSize applies when /props answers with n_ctx 0: routers
	// mask the server context, so probe falls back to the standard window.
	fallbackContextSize = 8192
)

// Probe queries GET /v1/models, GET /props, and POST /tokenize for FIM vocab
// detection. Every call is capped at 3s. Models failing is an error; props
// and tokenize failures degrade to their documented fallbacks.
func Probe(ctx context.Context, cfg *types.EndpointConfig) (*Resolved, error) {
	res := &Resolved{}

	models, err := probeModels(ctx, cfg)
	if err != nil {
		return res, err
	}
	res.Models = models
	res.ContextSize = probeContextSize(ctx, cfg)
	res.FIMTokens = probeFIMTokens(ctx, cfg, models)
	return res, nil
}

// ResolveModel picks a model id for role from a probe result ("auto"
// handling): the role's family preference order, first match wins inside
// each family, models whose dialect belongs to the other role never match.
// Empty means nothing usable for the role.
func ResolveModel(role Role, res *Resolved) string {
	if res == nil {
		return ""
	}
	order := []string{"fim-mellum", "fim-qwen", "fim-plain"}
	if role == RoleEdit {
		order = []string{"edit-zeta21", "edit-zeta2", "edit-sweep"}
	}
	for _, name := range order {
		for _, model := range res.Models {
			if dialectForModel(model.ID).name == name {
				return model.ID
			}
		}
	}
	return ""
}

func probeModels(ctx context.Context, cfg *types.EndpointConfig) ([]ModelInfo, error) {
	body, err := probeRequest(ctx, cfg, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("provider probe: /v1/models: %w", err)
	}
	var payload struct {
		Data []struct {
			ID     string          `json:"id"`
			Status json.RawMessage `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("provider probe: /v1/models: decode: %w", err)
	}
	models := make([]ModelInfo, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if entry.ID == "" {
			continue
		}
		models = append(models, ModelInfo{ID: entry.ID, State: statusValue(entry.Status)})
	}
	return models, nil
}

// statusValue flattens the two llama.cpp status shapes: plain llama-server
// sends a string, the multi-model router sends {"value": "...", ...}.
func statusValue(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Value
	}
	return ""
}

// probeContextSize reads n_ctx from /props. It returns 0 when the server
// never answered (unknown), the reported window otherwise, and the fallback
// window when the server answered with a masked n_ctx of 0.
func probeContextSize(ctx context.Context, cfg *types.EndpointConfig) int {
	body, err := probeRequest(ctx, cfg, http.MethodGet, "/props", nil)
	if err != nil {
		logger.Debug("provider probe: /props unavailable: %v", err)
		return 0
	}
	var props struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := json.Unmarshal(body, &props); err != nil {
		logger.Debug("provider probe: /props decode: %v", err)
		return 0
	}
	if props.DefaultGenerationSettings.NCtx <= 0 {
		return fallbackContextSize
	}
	return props.DefaultGenerationSettings.NCtx
}

func probeFIMTokens(ctx context.Context, cfg *types.EndpointConfig, models []ModelInfo) map[string]*types.FIMTokenConfig {
	tokens := make(map[string]*types.FIMTokenConfig)
	for _, model := range models {
		if preset := probeModelTokens(ctx, cfg, model.ID); preset != nil {
			tokens[model.ID] = preset
		}
	}
	return tokens
}

// probeModelTokens probes the family preset named by the model id first, then
// both presets for unknown names. A preset is confirmed when every candidate
// token string tokenizes to exactly one token under parse_special. A
// name-matched family keeps its preset when the probe cannot confirm it;
// unknown names get a preset only when the vocab confirms it. Edit dialects
// never consume FIM tokens, so they are not probed.
func probeModelTokens(ctx context.Context, cfg *types.EndpointConfig, model string) *types.FIMTokenConfig {
	d := dialectForModel(model)
	switch {
	case d.name == "fim-mellum":
		return mellumTokens()
	case d.name == "fim-qwen":
		return qwenTokens()
	case d.role == RoleEdit:
		return nil
	}
	for _, family := range []func() *types.FIMTokenConfig{mellumTokens, qwenTokens} {
		preset := family()
		if confirmPreset(ctx, cfg, model, preset) {
			return preset
		}
	}
	return nil
}

// confirmPreset probes every candidate token string of the preset itself and
// reports whether all of them are single vocab tokens.
func confirmPreset(ctx context.Context, cfg *types.EndpointConfig, model string, preset *types.FIMTokenConfig) bool {
	for _, candidate := range presetTokenStrings(preset) {
		tokens, err := tokenize(ctx, cfg, model, candidate)
		if err != nil {
			logger.Debug("provider probe: tokenize %q for %s: %v", candidate, model, err)
			return false
		}
		if len(tokens) != 1 {
			return false
		}
	}
	return true
}

func presetTokenStrings(preset *types.FIMTokenConfig) []string {
	var candidates []string
	for _, candidate := range []string{preset.Prefix, preset.Suffix, preset.Middle, preset.RepoName, preset.FileSep, preset.Filename} {
		if candidate != "" {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func tokenize(ctx context.Context, cfg *types.EndpointConfig, model, content string) ([]int, error) {
	body, err := probeRequest(ctx, cfg, http.MethodPost, "/tokenize", map[string]any{
		"model":         model,
		"content":       content,
		"parse_special": true,
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tokens []int `json:"tokens"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return payload.Tokens, nil
}

func probeRequest(ctx context.Context, cfg *types.EndpointConfig, method, path string, payload any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, string(data))
	}
	return data, nil
}
