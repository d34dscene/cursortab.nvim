package ctx

import (
	"context"
	"fmt"
	"time"
)

// cooperativeCollectTimeout is passed to material collectors that honor context
// cancellation.
const cooperativeCollectTimeout = 200 * time.Millisecond

// Collect executes the requested materials in order with one shared context.
// It also reports how many bytes the collected materials will add to the
// prompt, as consumed from the limits' shared budget.
func Collect(parent context.Context, input ContextSourceInput, requirements Materials) (Materials, int, error) {
	if len(requirements) == 0 {
		return nil, 0, nil
	}

	if input.Limits.ContextChars >= 0 {
		input.Budget = NewBudget(input.Limits.ContextChars)
	}

	ctx, cancel := context.WithTimeout(parent, cooperativeCollectTimeout)
	defer cancel()

	collected := make(Materials, len(requirements))
	for i, requirement := range requirements {
		material, err := requirement.collect(ctx, input)
		if err != nil {
			return nil, 0, fmt.Errorf("context material %T: %w", requirement, err)
		}
		collected[i] = material
	}

	used := 0
	if input.Budget != nil {
		used = input.Budget.Used()
	}
	return collected, used, nil
}
