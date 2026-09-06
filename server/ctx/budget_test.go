package ctx

import (
	"testing"

	"cursortab/assert"
)

func TestBudgetTakeGrantsUpToRemaining(t *testing.T) {
	budget := NewBudget(100)

	assert.Equal(t, 60, budget.Take(60), "full grant within budget")
	assert.Equal(t, 40, budget.Take(60), "partial grant clamps to remaining")
	assert.Equal(t, 0, budget.Remaining(), "budget exhausted")
	assert.Equal(t, 100, budget.Used(), "used tracks granted bytes")
	assert.Equal(t, 0, budget.Take(10), "nothing granted after exhaustion")
}

func TestBudgetZeroCapAllowsNothing(t *testing.T) {
	budget := NewBudget(0)

	assert.Equal(t, 0, budget.Take(50), "zero budget grants nothing")
	assert.Equal(t, 0, budget.Used(), "used stays zero")
}
