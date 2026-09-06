package provider

import (
	"cursortab/types"
	"cursortab/utils"
)

const (
	// defaultContextTokens bounds the prompt when context_size is 0. Small
	// models degrade quickly when overloaded, so the default is deliberately
	// conservative.
	defaultContextTokens = 1024
	// scaffoldTokenReserve covers FIM tokens, file headers, and markers that
	// the prompt adds around the window and materials.
	scaffoldTokenReserve = 32
	// maxMaterialsShare caps cross-file materials at this share of the input
	// budget, guaranteeing the current-file window a floor.
	maxMaterialsShare = 0.5
)

// InputTokens returns the token budget for the whole prompt input: the
// configured context window minus the generation reserve. context_size is the
// model's total context window; 0 selects the conservative default.
func InputTokens(config *types.ProviderConfig) int {
	if config == nil {
		return 0
	}
	contextSize := config.ProviderContextSize
	if contextSize == 0 {
		contextSize = defaultContextTokens
	}
	return max(contextSize-config.ProviderMaxTokens, 0)
}

// MaterialsTokens returns the token budget for cross-file context materials.
func MaterialsTokens(config *types.ProviderConfig) int {
	return int(float64(InputTokens(config)) * maxMaterialsShare)
}

// WindowTokens returns the token budget for the current-file window after
// materials and prompt scaffolding take their share. Zero means unbounded
// config (no trimming), matching the pre-budget behavior.
func WindowTokens(config *types.ProviderConfig, contextChars int) int {
	input := InputTokens(config)
	if input <= 0 {
		return 0
	}
	window := input - contextChars/utils.AvgCharsPerToken - scaffoldTokenReserve
	return max(window, 1)
}
