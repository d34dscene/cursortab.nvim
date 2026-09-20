package provider

import (
	"math"

	"cursortab/types"
)

// logprobSource is implemented by raw provider results that carry per-token
// log probabilities. openai.CompletionResult satisfies it.
type logprobSource interface {
	LogprobValues() []float64
}

// attachConfidence records the mean token logprob on the response when the raw
// result carries logprobs. It is a no-op otherwise, so providers that do not
// support logprobs stay unaffected.
func attachConfidence(response *types.CompletionResponse, raw any) {
	if response == nil || response.Confidence != nil {
		return
	}
	source, ok := raw.(logprobSource)
	if !ok {
		return
	}
	response.Confidence = meanLogprob(source.LogprobValues())
}

// meanLogprob averages the token logprobs. Impossible tokens (-Inf) are
// skipped so one forced token does not sink an otherwise confident completion.
// Returns nil when there is nothing to average.
func meanLogprob(logprobs []float64) *float64 {
	var sum float64
	var count int
	for _, logprob := range logprobs {
		if math.IsInf(logprob, -1) || math.IsNaN(logprob) {
			continue
		}
		sum += logprob
		count++
	}
	if count == 0 {
		return nil
	}
	mean := sum / float64(count)
	return &mean
}
