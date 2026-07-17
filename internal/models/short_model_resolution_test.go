package models

import (
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/assert"
)

func TestResolveUniqueAliasedBackendShortName(t *testing.T) {
	const (
		credentialName  = "mock-openai"
		syntheticDBCred = "db-model-image-deployment"
		publicModel     = "google/gemini-2.5-flash-image"
		backendModel    = "openai/gemini-2.5-flash-image"
	)
	manager := New(testhelpers.NewTestLogger(), 100, []config.ModelRPMConfig{
		{Name: backendModel, Credential: credentialName},
	})
	manager.SetModelAliases(map[string]string{
		publicModel: backendModel,
	})
	staticCredentials := []config.CredentialConfig{{
		Name: credentialName,
		Type: config.ProviderTypeOpenAI,
	}}
	manager.LoadModelsFromConfig(staticCredentials)

	resolved, ok := manager.ResolveUniqueAliasedBackendShortName("gemini-2.5-flash-image")

	assert.True(t, ok)
	assert.Equal(t, backendModel, resolved)

	// The migration stand also syncs the LiteLLM model-table row under its
	// public name. That public DB model shares the same final path segment, but
	// it is not an alias target and must not make backend resolution ambiguous.
	dbCredential := config.CredentialConfig{Name: syntheticDBCred, Type: config.ProviderTypeOpenAI}
	manager.UpdateDBModels([]config.ModelRPMConfig{{
		Name:       publicModel,
		Model:      backendModel,
		Credential: syntheticDBCred,
	}}, staticCredentials, append(staticCredentials, dbCredential))

	resolved, ok = manager.ResolveUniqueAliasedBackendShortName("gemini-2.5-flash-image")

	assert.True(t, ok)
	assert.Equal(t, backendModel, resolved)
}

func TestResolveUniqueAliasedBackendShortNameFailsClosed(t *testing.T) {
	const credentialName = "mock-openai"
	tests := []struct {
		name       string
		models     []config.ModelRPMConfig
		aliases    map[string]string
		requested  string
		wantResult string
	}{
		{
			name: "two configured alias targets share the same short name",
			models: []config.ModelRPMConfig{
				{Name: "openai/shared-image", Credential: credentialName},
				{Name: "vertex_ai/shared-image", Credential: credentialName},
			},
			aliases: map[string]string{
				"public/openai-image": "openai/shared-image",
				"public/vertex-image": "vertex_ai/shared-image",
			},
			requested:  "shared-image",
			wantResult: "shared-image",
		},
		{
			name: "exact configured short model wins over suffix fallback",
			models: []config.ModelRPMConfig{
				{Name: "shared-image", Credential: credentialName},
				{Name: "openai/shared-image", Credential: credentialName},
			},
			aliases: map[string]string{
				"public/openai-image": "openai/shared-image",
			},
			requested:  "shared-image",
			wantResult: "shared-image",
		},
		{
			name: "orphan alias target is not routable",
			models: []config.ModelRPMConfig{
				{Name: "some-other-image", Credential: credentialName},
			},
			aliases: map[string]string{
				"public/orphan-image": "openai/orphan-image",
			},
			requested:  "orphan-image",
			wantResult: "orphan-image",
		},
		{
			name: "already qualified name is never suffix resolved",
			models: []config.ModelRPMConfig{
				{Name: "openai/shared-image", Credential: credentialName},
			},
			aliases: map[string]string{
				"public/openai-image": "openai/shared-image",
			},
			requested:  "other/shared-image",
			wantResult: "other/shared-image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := New(testhelpers.NewTestLogger(), 100, tt.models)
			manager.SetModelAliases(tt.aliases)
			manager.LoadModelsFromConfig([]config.CredentialConfig{{
				Name: credentialName,
				Type: config.ProviderTypeOpenAI,
			}})

			resolved, ok := manager.ResolveUniqueAliasedBackendShortName(tt.requested)

			assert.False(t, ok)
			assert.Equal(t, tt.wantResult, resolved)
		})
	}
}
