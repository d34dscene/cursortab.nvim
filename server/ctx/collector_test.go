package ctx

import (
	"context"
	"testing"

	"cursortab/assert"
)

type testMaterial struct {
	name      string
	collectFn func(context.Context, ContextSourceInput) (material, error)
}

func (m testMaterial) collect(ctx context.Context, input ContextSourceInput) (material, error) {
	if m.collectFn != nil {
		return m.collectFn(ctx, input)
	}
	return collectedTestMaterial{name: m.name}, nil
}

type collectedTestMaterial struct {
	name string
}

func (m collectedTestMaterial) collect(context.Context, ContextSourceInput) (material, error) {
	return m, nil
}

func TestCollectRunsMaterialsInOrder(t *testing.T) {
	var calls []string

	collectOne := func(name string) func(context.Context, ContextSourceInput) (material, error) {
		return func(context.Context, ContextSourceInput) (material, error) {
			calls = append(calls, name)
			return collectedTestMaterial{name: name}, nil
		}
	}

	materials, _ := Collect(context.Background(), ContextSourceInput{}, Materials{
		testMaterial{name: "first", collectFn: collectOne("first")},
		testMaterial{name: "second", collectFn: collectOne("second")},
	})

	assert.Equal(t, []string{"first", "second"}, calls, "call order")
	assert.Len(t, 2, materials, "materials")
	first, ok := materials[0].(collectedTestMaterial)
	assert.True(t, ok, "first material type")
	second, ok := materials[1].(collectedTestMaterial)
	assert.True(t, ok, "second material type")
	assert.Equal(t, "first", first.name, "first material order")
	assert.Equal(t, "second", second.name, "second material order")
}

func TestCollectDegradesFailingMaterialToEmpty(t *testing.T) {
	materials, used := Collect(context.Background(), ContextSourceInput{}, Materials{
		testMaterial{name: "ok"},
		testMaterial{
			name: "broken",
			collectFn: func(context.Context, ContextSourceInput) (material, error) {
				return nil, context.DeadlineExceeded
			},
		},
	})

	assert.Len(t, 2, materials, "every request yields a material")
	first, ok := materials[0].(collectedTestMaterial)
	assert.True(t, ok, "healthy material collected")
	assert.Equal(t, "ok", first.name, "healthy material value")
	_, ok = materials[1].(testMaterial)
	assert.True(t, ok, "failing material degrades to its empty request")
	assert.Equal(t, 0, used, "no budget means no usage tracking")
}

func TestCollectImposesNoDeadlineOnMaterials(t *testing.T) {
	var sawDeadline bool

	_, _ = Collect(context.Background(), ContextSourceInput{}, Materials{
		testMaterial{
			name: "observer",
			collectFn: func(ctx context.Context, _ ContextSourceInput) (material, error) {
				_, sawDeadline = ctx.Deadline()
				return collectedTestMaterial{name: "observer"}, nil
			},
		},
	})

	assert.False(t, sawDeadline, "collect passes the parent context without a synthetic timeout")
}
