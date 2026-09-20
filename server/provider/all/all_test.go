package all

import (
	"testing"

	"cursortab/assert"
	"cursortab/provider"
	"cursortab/types"
)

// Copilot and windsurf are absent on purpose: they need the editor buffer or a
// live LSP shim, so their callers construct them directly.
var expected = []string{"fim", "inline", "mercuryapi", "sweep", "zeta", "zeta-2", "zeta-2.1"}

func TestRegistersConfigBuildableProviders(t *testing.T) {
	assert.Equal(t, expected, provider.Names(), "registered provider names")
}

func TestEveryRegisteredNameBuilds(t *testing.T) {
	for _, name := range expected {
		built, err := provider.Build(name, &types.ProviderConfig{})
		assert.NoError(t, err, "build "+name)
		assert.NotNil(t, built, "provider built for "+name)
	}
}
