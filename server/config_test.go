package main

import (
	"encoding/json"
	"testing"
)

func validConfig() Config {
	return Config{
		LogLevel: "info",
		StateDir: "/tmp/cursortab",
		Provider: ProviderConfig{
			Endpoint: EndpointConfig{
				URL:       "http://localhost:8000",
				Model:     "auto",
				MaxTokens: 64,
			},
			RetrievalMaxChunks: 8,
			MinConfidence:      -1.5,
		},
		Behavior: BehaviorConfig{
			IdleCompletionDelay: 50,
			TextChangeDebounce:  50,
			MaxVisibleLines:     12,
			CompleteInInsert:    true,
			CompleteInNormal:    true,
		},
	}
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	config := validConfig()
	config.applyDefaults()

	if err := config.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateAcceptsRetrievalAndGatingConfig(t *testing.T) {
	config := validConfig()
	config.Provider.RetrievalEnabled = true
	config.Provider.Logprobs = true
	config.applyDefaults()

	if err := config.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateAcceptsDisabledGating(t *testing.T) {
	config := validConfig()
	config.Provider.MinConfidence = 0
	config.applyDefaults()

	if err := config.Validate(); err != nil {
		t.Fatalf("disabled gate rejected: %v", err)
	}
}

func TestValidateRejectsPositiveMinConfidence(t *testing.T) {
	config := validConfig()
	config.Provider.MinConfidence = 2.0
	config.applyDefaults()

	if err := config.Validate(); err == nil {
		t.Fatal("positive min_confidence accepted")
	}
}

func TestValidateRejectsNegativeRetrievalChunks(t *testing.T) {
	config := validConfig()
	config.Provider.RetrievalMaxChunks = -1
	config.applyDefaults()

	if err := config.Validate(); err == nil {
		t.Fatal("negative retrieval_max_chunks accepted")
	}
}

func TestValidateRequiresEndpointURL(t *testing.T) {
	config := validConfig()
	config.Provider.Endpoint.URL = ""
	config.applyDefaults()

	if err := config.Validate(); err == nil {
		t.Fatal("missing provider.endpoint.url accepted")
	}
}

// §8: completion_timeout <= 0 is invalid config and fails at startup.
func TestValidateRejectsNonPositiveCompletionTimeout(t *testing.T) {
	for _, timeout := range []int{0, -1} {
		config := validConfig()
		config.Provider.Endpoint.TimeoutMs = timeout

		if err := config.Validate(); err == nil {
			t.Fatalf("completion_timeout %d accepted", timeout)
		}
	}
}

func TestValidateRejectsNonPositiveNextEditTimeout(t *testing.T) {
	config := validConfig()
	config.applyDefaults()
	config.Provider.NextEdit = &EndpointConfig{
		URL:       "http://localhost:8000",
		Model:     "auto",
		TimeoutMs: -1,
	}

	if err := config.Validate(); err == nil {
		t.Fatal("negative next_edit timeout accepted")
	}
}

func TestValidateRequiresNextEditURLInDualMode(t *testing.T) {
	config := validConfig()
	config.applyDefaults()
	config.Provider.NextEdit = &EndpointConfig{Model: "auto"}

	if err := config.Validate(); err == nil {
		t.Fatal("dual mode without provider.next_edit.url accepted")
	}
}

func TestValidateRejectsContextSizeAtOrBelowMaxTokens(t *testing.T) {
	config := validConfig()
	config.Provider.ContextSize = 64
	config.applyDefaults()

	if err := config.Validate(); err == nil {
		t.Fatal("context_size <= max_tokens accepted")
	}
}

func TestValidateRequiresCompleteFIMTokens(t *testing.T) {
	config := validConfig()
	config.applyDefaults()
	config.Provider.FIMTokens = &FIMTokensConfig{Prefix: "<fim>"}

	if err := config.Validate(); err == nil {
		t.Fatal("fim_tokens without suffix/middle accepted")
	}
}

// TestWireShapeBindsDaemonLuaKeys pins the exact JSON keys daemon.lua emits.
// encoding/json matches tags case-insensitively but not punctuation, so an
// untagged field silently misses keys like api_key.
func TestWireShapeBindsDaemonLuaKeys(t *testing.T) {
	const singleMode = `{
		"ns_id": 1,
		"log_level": "info",
		"state_dir": "/home/u/.local/state/cursortab",
		"provider": {
			"endpoint": {"url": "http://localhost:8000", "api_key": "secret", "model": "auto", "max_tokens": 64},
			"context_size": 8192,
			"fim_tokens": {"prefix": "<fim>", "suffix": "</fim>", "middle": "<middle>", "repo_name": "repo", "file_sep": "sep", "filename": "file", "suffix_first": true},
			"retrieval_enabled": true,
			"retrieval_max_chunks": 8,
			"logprobs": true,
			"min_confidence": -1.5
		},
		"behavior": {
			"idle_completion_delay": 400,
			"text_change_debounce": 35,
			"max_visible_lines": 12,
			"disabled_in": ["comment"],
			"complete_in_insert": true,
			"complete_in_normal": false,
			"cursor_prediction": {"enabled": true, "auto_advance": false, "proximity_threshold": 3}
		},
		"debug": {"immediate_shutdown": true}
	}`

	var config Config
	if err := json.Unmarshal([]byte(singleMode), &config); err != nil {
		t.Fatalf("single mode config rejected: %v", err)
	}
	if config.NsID != 1 || config.StateDir == "" || config.LogLevel != "info" {
		t.Fatalf("top level not bound: %+v", config)
	}
	endpoint := config.Provider.Endpoint
	if endpoint.URL != "http://localhost:8000" || endpoint.APIKey != "secret" ||
		endpoint.Model != "auto" || endpoint.MaxTokens != 64 {
		t.Fatalf("provider.endpoint not bound: %+v", endpoint)
	}
	if config.Provider.NextEdit != nil {
		t.Fatal("single mode must leave provider.next_edit nil")
	}
	if config.Provider.ContextSize != 8192 || !config.Provider.RetrievalEnabled ||
		config.Provider.RetrievalMaxChunks != 8 || !config.Provider.Logprobs ||
		config.Provider.MinConfidence != -1.5 {
		t.Fatalf("provider table not bound: %+v", config.Provider)
	}
	tokens := config.Provider.FIMTokens
	if tokens == nil || tokens.Prefix != "<fim>" || tokens.Suffix != "</fim>" ||
		tokens.Middle != "<middle>" || tokens.RepoName != "repo" ||
		tokens.FileSep != "sep" || tokens.Filename != "file" || !tokens.SuffixFirst {
		t.Fatalf("fim_tokens not bound: %+v", tokens)
	}
	if !config.Behavior.CompleteInInsert || config.Behavior.CompleteInNormal ||
		config.Behavior.IdleCompletionDelay != 400 || config.Behavior.TextChangeDebounce != 35 ||
		config.Behavior.MaxVisibleLines != 12 || len(config.Behavior.DisabledIn) != 1 ||
		!config.Behavior.CursorPrediction.Enabled || config.Behavior.CursorPrediction.AutoAdvance ||
		config.Behavior.CursorPrediction.ProximityThreshold != 3 {
		t.Fatalf("behavior not bound: %+v", config.Behavior)
	}
	if !config.Debug.ImmediateShutdown {
		t.Fatal("debug.immediate_shutdown not bound")
	}

	// Dual mode only adds provider.next_edit.
	const dual = `{"ns_id":1,"log_level":"info","state_dir":"/s","provider":{"endpoint":{"url":"http://localhost:8000","api_key":"secret","model":"auto","max_tokens":64},"next_edit":{"url":"http://localhost:8000","api_key":"secret","model":"auto","max_tokens":256},"context_size":0,"retrieval_enabled":false,"retrieval_max_chunks":0,"logprobs":false,"min_confidence":0},"behavior":{"idle_completion_delay":400,"text_change_debounce":35,"max_visible_lines":12,"disabled_in":[],"complete_in_insert":true,"complete_in_normal":true,"cursor_prediction":{"enabled":true,"auto_advance":true,"proximity_threshold":3}},"debug":{"immediate_shutdown":false}}`
	var dualConfig Config
	if err := json.Unmarshal([]byte(dual), &dualConfig); err != nil {
		t.Fatalf("dual mode config rejected: %v", err)
	}
	if dualConfig.Provider.NextEdit == nil ||
		dualConfig.Provider.NextEdit.Model != "auto" ||
		dualConfig.Provider.NextEdit.MaxTokens != 256 ||
		dualConfig.Provider.NextEdit.URL != "http://localhost:8000" ||
		dualConfig.Provider.NextEdit.APIKey != "secret" {
		t.Fatalf("provider.next_edit not bound: %+v", dualConfig.Provider.NextEdit)
	}
}

func TestApplyDefaultsResolvesRoleTimeoutsAndDiffTokens(t *testing.T) {
	config := validConfig()
	config.Provider.NextEdit = &EndpointConfig{
		URL:   "http://localhost:8000",
		Model: "auto",
	}

	config.applyDefaults()

	if config.Provider.Endpoint.TimeoutMs != defaultTypeTimeoutMs {
		t.Fatalf("type timeout = %d, want %d", config.Provider.Endpoint.TimeoutMs, defaultTypeTimeoutMs)
	}
	if config.Provider.NextEdit.TimeoutMs != defaultEditTimeoutMs {
		t.Fatalf("edit timeout = %d, want %d", config.Provider.NextEdit.TimeoutMs, defaultEditTimeoutMs)
	}
	if config.Provider.MaxDiffHistoryTokens != defaultMaxDiffHistoryTokens {
		t.Fatalf("max_diff_history_tokens = %d, want %d", config.Provider.MaxDiffHistoryTokens, defaultMaxDiffHistoryTokens)
	}
}
