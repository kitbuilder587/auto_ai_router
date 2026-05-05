package responses

import (
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestHasNativeResponsesForModel_AllModels(t *testing.T) {
	// Register a provider with no model filter (all models supported).
	const testProviderAll config.ProviderType = "test_all_models"
	RegisterProviderResponses(testProviderAll, func(mode ResponsesRequestMode) ProviderResponses {
		return nil
	})

	assert.True(t, HasNativeResponsesForModel(testProviderAll, "any-model"))
	assert.True(t, HasNativeResponsesForModel(testProviderAll, "other-model"))
	assert.True(t, HasNativeResponses(testProviderAll))
}

func TestHasNativeResponsesForModel_WithFilter(t *testing.T) {
	// Register a provider that only supports models starting with "supported.".
	const testProviderFiltered config.ProviderType = "test_filtered_models"
	RegisterProviderResponsesForModel(
		testProviderFiltered,
		func(mode ResponsesRequestMode) ProviderResponses { return nil },
		func(modelID string) bool { return len(modelID) > 0 && modelID[:9] == "supported" },
	)

	assert.True(t, HasNativeResponsesForModel(testProviderFiltered, "supported.model-v1"))
	assert.False(t, HasNativeResponsesForModel(testProviderFiltered, "unsupported.model"))
	// HasNativeResponses should return false because a model filter is set.
	assert.False(t, HasNativeResponses(testProviderFiltered))
}

func TestHasNativeResponsesForModel_UnknownProvider(t *testing.T) {
	const unknownProvider config.ProviderType = "no_such_provider_xyz"
	assert.False(t, HasNativeResponsesForModel(unknownProvider, "any-model"))
	assert.False(t, HasNativeResponses(unknownProvider))
}
