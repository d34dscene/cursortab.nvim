package engine

import (
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func confidenceResponse(confidence *float64) *types.CompletionResponse {
	response := completionResponse(&types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	})
	response.Confidence = confidence
	return response
}

func engineWithMinConfidence(minConfidence float64) *Engine {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	eng := createTestEngine(buf, newMockProvider(), newMockClock())
	eng.config.MinConfidence = minConfidence
	return eng
}

func TestLowConfidenceCompletionIsSuppressed(t *testing.T) {
	eng := engineWithMinConfidence(-1.0)
	low := -3.5

	outcome := eng.processCompletionWithManual(confidenceResponse(&low), false)

	assert.Equal(t, completionSuppressed, outcome, "below-floor completion dropped")
}

func TestConfidentCompletionIsShown(t *testing.T) {
	eng := engineWithMinConfidence(-1.0)
	high := -0.2

	outcome := eng.processCompletionWithManual(confidenceResponse(&high), false)

	assert.Equal(t, completionShown, outcome, "above-floor completion shown")
}

func TestConfidenceGateDisabledWhenFloorIsZero(t *testing.T) {
	eng := engineWithMinConfidence(0)
	low := -10.0

	outcome := eng.processCompletionWithManual(confidenceResponse(&low), false)

	assert.Equal(t, completionShown, outcome, "no floor means no gate")
}

func TestConfidenceGateIgnoresMissingConfidence(t *testing.T) {
	eng := engineWithMinConfidence(-1.0)

	outcome := eng.processCompletionWithManual(confidenceResponse(nil), false)

	assert.Equal(t, completionShown, outcome, "provider without logprobs is never gated")
}

func TestManualTriggerBypassesConfidenceGate(t *testing.T) {
	eng := engineWithMinConfidence(-1.0)
	low := -10.0

	outcome := eng.processCompletionWithManual(confidenceResponse(&low), true)

	assert.Equal(t, completionShown, outcome, "manual trigger bypasses the floor")
}
