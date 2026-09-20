package provider

import (
	"math"
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func TestMeanLogprob(t *testing.T) {
	cases := []struct {
		name     string
		logprobs []float64
		want     *float64
	}{
		{name: "empty", logprobs: nil, want: nil},
		{name: "single", logprobs: []float64{-0.5}, want: ptr(-0.5)},
		{name: "mean", logprobs: []float64{-0.2, -0.8}, want: ptr(-0.5)},
		{name: "skips impossible tokens", logprobs: []float64{math.Inf(-1), -1.0}, want: ptr(-1.0)},
		{name: "all impossible", logprobs: []float64{math.Inf(-1)}, want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := meanLogprob(tc.logprobs)
			if tc.want == nil {
				assert.Nil(t, got, "expected nil")
				return
			}
			assert.NotNil(t, got, "expected a value")
			assert.Equal(t, *tc.want, *got, "mean")
		})
	}
}

type stubRaw struct {
	logprobs []float64
}

func (s stubRaw) LogprobValues() []float64 { return s.logprobs }

func TestAttachConfidence(t *testing.T) {
	response := &types.CompletionResponse{}
	attachConfidence(response, stubRaw{logprobs: []float64{-0.4, -0.6}})
	assert.NotNil(t, response.Confidence, "confidence set")
	assert.Equal(t, -0.5, *response.Confidence, "mean attached")

	// Raw results without logprobs leave confidence nil.
	other := &types.CompletionResponse{}
	attachConfidence(other, struct{}{})
	assert.Nil(t, other.Confidence, "no logprobs means no confidence")
}

func ptr(v float64) *float64 { return &v }
