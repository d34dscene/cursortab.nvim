package provider

import (
	"strings"
	"testing"

	"cursortab/assert"
	sourcectx "cursortab/ctx"
	"cursortab/types"
	"cursortab/utils"
)

func TestInputTokens(t *testing.T) {
	tests := []struct {
		name   string
		config *types.ProviderConfig
		want   int
	}{
		{"nil config", nil, 0},
		{"unset falls back to default", &types.ProviderConfig{ProviderMaxTokens: 512}, 512},
		{"configured minus generation reserve", &types.ProviderConfig{ProviderContextSize: 8192, ProviderMaxTokens: 512}, 7680},
		{"degenerate config clamps to zero", &types.ProviderConfig{ProviderContextSize: 256, ProviderMaxTokens: 512}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, InputTokens(tt.config), "input tokens")
		})
	}
}

func TestMaterialsTokensIsAtMostHalfOfInput(t *testing.T) {
	config := &types.ProviderConfig{ProviderContextSize: 8192, ProviderMaxTokens: 512}
	assert.Equal(t, InputTokens(config)/2, MaterialsTokens(config), "materials share")
}

func TestWindowTokensShrinksWithContextAndKeepsFloor(t *testing.T) {
	config := &types.ProviderConfig{ProviderContextSize: 8192, ProviderMaxTokens: 512}
	input := InputTokens(config)

	withoutContext := WindowTokens(config, 0)
	assert.Equal(t, input-scaffoldTokenReserve, withoutContext, "window without materials")

	withContext := WindowTokens(config, input*utils.AvgCharsPerToken/2)
	assert.Equal(t, input-input/2-scaffoldTokenReserve, withContext, "window with materials")

	exhausted := WindowTokens(config, input*utils.AvgCharsPerToken)
	assert.Equal(t, 1, exhausted, "window keeps a one-token floor")

	assert.Equal(t, 0, WindowTokens(nil, 0), "nil config stays untrimmed")
}

func TestPrepareRequestStateWindowFitsBudget(t *testing.T) {
	config := &types.ProviderConfig{ProviderContextSize: 2048, ProviderMaxTokens: 512}
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = strings.Repeat("x", 80)
	}
	input := sourcectx.CompletionInput{
		Current: sourcectx.CurrentSnapshot{
			File:   sourcectx.FileSnapshot{Lines: lines},
			Cursor: sourcectx.CursorPosition{Row: 500, Col: 0},
		},
		ContextChars: 512 * utils.AvgCharsPerToken,
	}

	state := prepareRequestState(input, config)
	windowChars := 0
	for _, line := range state.Window.Lines {
		windowChars += len(line) + 1
	}

	budget := utils.EstimateCharsFromTokens(WindowTokens(config, input.ContextChars))
	maxLineChars := len(lines[0]) + 1
	// The balanced window may overshoot by one line on each side of the
	// cursor; the cursor line itself is always included in full.
	slack := 2*maxLineChars + maxLineChars
	assert.True(t, windowChars <= budget+slack, "window stays within its budget")
}
