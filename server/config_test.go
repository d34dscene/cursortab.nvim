package main

import "testing"

func validConfig() Config {
	return Config{
		LogLevel: "info",
		Behavior: BehaviorConfig{
			IdleCompletionDelay: 50,
			TextChangeDebounce:  50,
			MaxVisibleLines:     12,
		},
		Provider: ProviderConfig{
			Type:               "inline",
			CompletionPath:     "/v1/completions",
			MaxTokens:          64,
			CompletionTimeout:  5000,
			RetrievalMaxChunks: 8,
			MinConfidence:      -1.5,
		},
	}
}

func TestValidateAcceptsRetrievalAndGatingConfig(t *testing.T) {
	config := validConfig()
	config.Provider.RetrievalEnabled = true
	config.Provider.Logprobs = true

	if err := config.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateAcceptsDisabledGating(t *testing.T) {
	config := validConfig()
	config.Provider.MinConfidence = 0

	if err := config.Validate(); err != nil {
		t.Fatalf("disabled gate rejected: %v", err)
	}
}

func TestValidateRejectsPositiveMinConfidence(t *testing.T) {
	config := validConfig()
	config.Provider.MinConfidence = 2.0

	if err := config.Validate(); err == nil {
		t.Fatal("positive min_confidence accepted")
	}
}

func TestValidateRejectsNegativeRetrievalChunks(t *testing.T) {
	config := validConfig()
	config.Provider.RetrievalMaxChunks = -1

	if err := config.Validate(); err == nil {
		t.Fatal("negative retrieval_max_chunks accepted")
	}
}
