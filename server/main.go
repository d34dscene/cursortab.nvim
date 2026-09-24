package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"cursortab/logger"
)

// Version is the cursortab server version. It is updated automatically by the release workflow.
var Version = "0.8.0" // AUTO-UPDATED by release workflow

// Role timeouts and context size applied when the config leaves them unset
// (types.EndpointConfig documents 0 as "role default").
const (
	defaultTypeTimeoutMs        = 6000
	defaultEditTimeoutMs        = 20000
	defaultContextSize          = 8192
	defaultMaxDiffHistoryTokens = 512
)

// CursorPredictionConfig holds cursor prediction settings
type CursorPredictionConfig struct {
	Enabled            bool `json:"enabled"`
	AutoAdvance        bool `json:"auto_advance"`
	ProximityThreshold int  `json:"proximity_threshold"`
}

// BehaviorConfig holds timing and behavior settings
type BehaviorConfig struct {
	IdleCompletionDelay int                    `json:"idle_completion_delay"` // in milliseconds
	TextChangeDebounce  int                    `json:"text_change_debounce"`  // in milliseconds
	MaxVisibleLines     int                    `json:"max_visible_lines"`     // max visible lines per completion (0 to disable)
	CursorPrediction    CursorPredictionConfig `json:"cursor_prediction"`
	DisabledIn          []string               `json:"disabled_in"`
	CompleteInInsert    bool                   `json:"complete_in_insert"`
	CompleteInNormal    bool                   `json:"complete_in_normal"`
}

// FIMTokensConfig holds FIM token settings
type FIMTokensConfig struct {
	Prefix      string `json:"prefix"`
	Suffix      string `json:"suffix"`
	Middle      string `json:"middle"`
	RepoName    string `json:"repo_name"`
	FileSep     string `json:"file_sep"`
	Filename    string `json:"filename"`
	SuffixFirst bool   `json:"suffix_first"`
}

// EndpointConfig is one model endpoint as daemon.lua emits it. api_key is
// the resolved key value, timeout_ms is never sent (0 = role default).
type EndpointConfig struct {
	URL       string `json:"url"`
	APIKey    string `json:"api_key"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

// ProviderConfig is the provider table on the wire. next_edit appears only
// in dual mode. max_diff_history_tokens is Go-side optional (Lua omits it).
type ProviderConfig struct {
	Endpoint             EndpointConfig   `json:"endpoint"`
	NextEdit             *EndpointConfig  `json:"next_edit,omitempty"`
	ContextSize          int              `json:"context_size"`
	FIMTokens            *FIMTokensConfig `json:"fim_tokens"`
	RetrievalEnabled     bool             `json:"retrieval_enabled"`
	RetrievalMaxChunks   int              `json:"retrieval_max_chunks"`
	Logprobs             bool             `json:"logprobs"`
	MinConfidence        float64          `json:"min_confidence"`
	MaxDiffHistoryTokens int              `json:"max_diff_history_tokens"`
}

// DebugConfig holds debug settings
type DebugConfig struct {
	ImmediateShutdown bool `json:"immediate_shutdown"`
}

// Config is the main configuration structure, mirroring the JSON daemon.lua
// sends in CURSORTAB_CONFIG.
type Config struct {
	NsID     int            `json:"ns_id"`
	LogLevel string         `json:"log_level"`
	StateDir string         `json:"state_dir"`
	Provider ProviderConfig `json:"provider"`
	Behavior BehaviorConfig `json:"behavior"`
	Debug    DebugConfig    `json:"debug"`
}

// validateEnum checks that value is one of the valid options for the named field.
func validateEnum(value, field string, valid []string) error {
	if slices.Contains(valid, value) {
		return nil
	}
	return fmt.Errorf("invalid %s %q: must be one of %s", field, value, strings.Join(valid, ", "))
}

// Validate checks that the config has valid values. All config comes from
// the Lua client. applyDefaults has already resolved role defaults, so any
// remaining non-positive completion timeout is invalid config.
func (c *Config) Validate() error {
	if err := validateEnum(c.LogLevel, "log_level", []string{"trace", "debug", "info", "warn", "error"}); err != nil {
		return err
	}
	if c.Provider.Endpoint.URL == "" {
		return fmt.Errorf("provider.endpoint.url is required")
	}
	if c.Provider.Endpoint.TimeoutMs <= 0 {
		return fmt.Errorf("completion_timeout must be > 0, got %d", c.Provider.Endpoint.TimeoutMs)
	}
	if c.Provider.NextEdit != nil {
		if c.Provider.NextEdit.URL == "" {
			return fmt.Errorf("provider.next_edit.url is required in dual mode")
		}
		if c.Provider.NextEdit.TimeoutMs <= 0 {
			return fmt.Errorf("next_edit completion_timeout must be > 0, got %d", c.Provider.NextEdit.TimeoutMs)
		}
	}
	if c.Behavior.IdleCompletionDelay < -1 {
		return fmt.Errorf("invalid behavior.idle_completion_delay %d: must be >= -1", c.Behavior.IdleCompletionDelay)
	}
	if c.Behavior.TextChangeDebounce < -1 {
		return fmt.Errorf("invalid behavior.text_change_debounce %d: must be >= -1", c.Behavior.TextChangeDebounce)
	}
	if c.Behavior.MaxVisibleLines < 0 {
		return fmt.Errorf("invalid behavior.max_visible_lines %d: must be >= 0", c.Behavior.MaxVisibleLines)
	}
	if c.Provider.ContextSize < 0 {
		return fmt.Errorf("invalid provider.context_size %d: must be >= 0", c.Provider.ContextSize)
	}
	maxTokens := c.Provider.Endpoint.MaxTokens
	if maxTokens < 0 {
		return fmt.Errorf("invalid provider.endpoint.max_tokens %d: must be >= 0", maxTokens)
	}
	if c.Provider.NextEdit != nil && c.Provider.NextEdit.MaxTokens < 0 {
		return fmt.Errorf("invalid provider.next_edit.max_tokens %d: must be >= 0", c.Provider.NextEdit.MaxTokens)
	}
	if c.Provider.ContextSize > 0 && c.Provider.ContextSize <= maxTokens {
		return fmt.Errorf(
			"invalid provider.context_size %d: must be greater than provider.endpoint.max_tokens %d (max_tokens is reserved for generation on top of the prompt budget)",
			c.Provider.ContextSize, maxTokens)
	}
	if c.Provider.MaxDiffHistoryTokens < 0 {
		return fmt.Errorf("invalid provider.max_diff_history_tokens %d: must be >= 0", c.Provider.MaxDiffHistoryTokens)
	}
	if c.Provider.RetrievalMaxChunks < 0 {
		return fmt.Errorf("invalid provider.retrieval_max_chunks %d: must be >= 0", c.Provider.RetrievalMaxChunks)
	}
	if c.Provider.MinConfidence > 0 {
		return fmt.Errorf("invalid provider.min_confidence %v: must be <= 0 (mean token logprob is always <= 0, 0 disables the gate)", c.Provider.MinConfidence)
	}

	// When fim_tokens is configured, prefix/suffix/middle must all be non-empty.
	// Absence (nil) signals prompt+suffix mode and requires no validation.
	if c.Provider.FIMTokens != nil {
		if c.Provider.FIMTokens.Prefix == "" {
			return fmt.Errorf("invalid provider.fim_tokens.prefix: must be non-empty")
		}
		if c.Provider.FIMTokens.Suffix == "" {
			return fmt.Errorf("invalid provider.fim_tokens.suffix: must be non-empty")
		}
		if c.Provider.FIMTokens.Middle == "" {
			return fmt.Errorf("invalid provider.fim_tokens.middle: must be non-empty")
		}
	}

	return nil
}

// applyDefaults resolves the endpoint role defaults documented in
// types.EndpointConfig. Lua never sends timeout_ms or
// max_diff_history_tokens.
func (c *Config) applyDefaults() {
	if c.Provider.Endpoint.TimeoutMs == 0 {
		c.Provider.Endpoint.TimeoutMs = defaultTypeTimeoutMs
	}
	if c.Provider.MaxDiffHistoryTokens == 0 {
		c.Provider.MaxDiffHistoryTokens = defaultMaxDiffHistoryTokens
	}
	if c.Provider.NextEdit != nil && c.Provider.NextEdit.TimeoutMs == 0 {
		c.Provider.NextEdit.TimeoutMs = defaultEditTimeoutMs
	}
}

type ServerMode string

const (
	ModeDaemon ServerMode = "daemon"
	ModeClient ServerMode = "client"
)

// ensureStateDir creates the state directory if it doesn't exist
func ensureStateDir(stateDir string) {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		logger.Fatal("error creating state directory: %v", err)
	}
}

// Setup logger to log to a file in the state directory
// Caller must defer logger.Close()
func setupLogger(stateDir, logLevel string) *logger.LimitedLogger {
	ensureStateDir(stateDir)
	logPath := filepath.Join(stateDir, "cursortab.log")

	f, err := os.OpenFile(logPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		logger.Fatal("error opening file: %v", err)
	}

	level := logger.ParseLogLevel(logLevel)
	return logger.NewLimitedLogger(f, level)
}

func getPidPath(stateDir string) string {
	return filepath.Join(stateDir, "cursortab.pid")
}

// loadConfig parses config from CURSORTAB_CONFIG env var.
// Uses standard log package since this runs before our logger is initialized.
func loadConfig() Config {
	var config Config
	if err := json.Unmarshal([]byte(os.Getenv("CURSORTAB_CONFIG")), &config); err != nil {
		log.Fatalf("invalid config JSON: %v", err)
	}

	config.applyDefaults()

	if err := config.Validate(); err != nil {
		log.Fatalf("config validation failed: %v", err)
	}

	return config
}

func runDaemon() {
	// Load config first to get state_dir
	config := loadConfig()

	// Setup logger with state_dir from config
	ll := setupLogger(config.StateDir, config.LogLevel)
	defer ll.Close()

	daemon, err := NewDaemon(config)
	if err != nil {
		logger.Fatal("error creating daemon: %v", err)
	}

	if err := daemon.Start(); err != nil {
		logger.Fatal("error starting daemon: %v", err)
	}
}

func runClient() {
	config := loadConfig()
	client := NewClient(config.StateDir)

	if err := client.Connect(); err != nil {
		logger.Fatal("error connecting to daemon: %v", err)
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(Version)
		return
	}

	var mode = ModeClient

	// Check command line arguments
	if len(os.Args) > 1 && os.Args[1] == "--daemon" {
		mode = ModeDaemon
	}

	switch mode {
	case ModeDaemon:
		runDaemon()
	case ModeClient:
		runClient()
	}
}
