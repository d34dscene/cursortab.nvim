package provider

import (
	"testing"

	"cursortab/assert"
	"cursortab/engine"
	"cursortab/types"
)

func TestRegistryBuildsRegisteredProvider(t *testing.T) {
	Register("registry-test", func(*types.ProviderConfig) engine.Provider { return nil })

	provider, err := Build("registry-test", &types.ProviderConfig{})
	assert.NoError(t, err, "build registered provider")
	assert.Nil(t, provider, "factory result returned")
}

func TestRegistryRejectsUnknownProvider(t *testing.T) {
	_, err := Build("not-a-provider", &types.ProviderConfig{})
	assert.Error(t, err, "unknown provider rejected")
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	Register("registry-dup", func(*types.ProviderConfig) engine.Provider { return nil })

	defer func() {
		assert.NotNil(t, recover(), "duplicate registration panics")
	}()
	Register("registry-dup", func(*types.ProviderConfig) engine.Provider { return nil })
}
