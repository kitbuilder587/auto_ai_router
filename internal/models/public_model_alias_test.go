package models

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type livePublicModelSurfaceFixture struct {
	ConfiguredDeploymentIdentities struct {
		Count        int      `json:"count"`
		PublicModels []string `json:"public_models"`
	} `json:"configured_deployment_identities"`
	LiveAliasSurface struct {
		Count                     int `json:"count"`
		DistinctConfiguredTargets int `json:"distinct_configured_targets"`
		Aliases                   []struct {
			Alias                 string `json:"alias"`
			ConfiguredPublicModel string `json:"configured_public_model"`
			BackendModel          string `json:"backend_model"`
			Mode                  string `json:"mode"`
		} `json:"aliases"`
	} `json:"live_alias_surface"`
	EffectivePublicSurface struct {
		Count        int      `json:"count"`
		PublicModels []string `json:"public_models"`
	} `json:"effective_public_surface"`
	AcceptedUnadvertisedIdentifiers struct {
		Count                     int `json:"count"`
		RecencySubsetLast24HCount int `json:"recency_subset_last_24h_count"`
		Identifiers               []struct {
			Identifier            string `json:"identifier"`
			ConfiguredPublicModel string `json:"configured_public_model"`
			BackendModel          string `json:"backend_model"`
			DeploymentMode        string `json:"deployment_mode"`
			ReplayKind            string `json:"replay_kind"`
			Path                  string `json:"path"`
		} `json:"identifiers"`
	} `json:"accepted_unadvertised_identifiers"`
	IngressCompatibilitySurface struct {
		Count       int      `json:"count"`
		Identifiers []string `json:"identifiers"`
	} `json:"ingress_compatibility_surface"`
	ExcludedRouteOnlyIdentifiers struct {
		Count       int `json:"count"`
		Identifiers []struct {
			Identifier            string `json:"identifier"`
			ConfiguredPublicModel string `json:"configured_public_model"`
			BackendModel          string `json:"backend_model"`
		} `json:"identifiers"`
	} `json:"excluded_route_only_identifiers"`
}

func TestExplicitClientModelSurfaceIsExactlyAdvertisedPlusAccepted(t *testing.T) {
	fixture := loadLivePublicModelSurfaceFixture(t)
	require.Equal(t, 107, fixture.ConfiguredDeploymentIdentities.Count)
	require.Equal(t, 144, fixture.EffectivePublicSurface.Count)
	require.Equal(t, 35, fixture.AcceptedUnadvertisedIdentifiers.Count)
	require.Equal(t, 179, fixture.IngressCompatibilitySurface.Count)
	require.Equal(t, 1, fixture.ExcludedRouteOnlyIdentifiers.Count)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	credential := config.CredentialConfig{Name: "provider", Type: config.ProviderTypeOpenAI}
	backendByCanonical := make(map[string]string, fixture.ConfiguredDeploymentIdentities.Count)
	for index, canonical := range fixture.ConfiguredDeploymentIdentities.PublicModels {
		backendByCanonical[canonical] = fmt.Sprintf("internal/backend-%03d", index)
	}
	for _, row := range fixture.LiveAliasSurface.Aliases {
		backendByCanonical[row.ConfiguredPublicModel] = row.BackendModel
	}
	for _, row := range fixture.AcceptedUnadvertisedIdentifiers.Identifiers {
		backendByCanonical[row.ConfiguredPublicModel] = row.BackendModel
	}
	for _, row := range fixture.ExcludedRouteOnlyIdentifiers.Identifiers {
		backendByCanonical[row.ConfiguredPublicModel] = row.BackendModel
	}

	backendSet := make(map[string]struct{}, len(backendByCanonical))
	providerAliases := make(map[string]string, len(backendByCanonical))
	dbModels := make([]config.ModelRPMConfig, 0, len(backendByCanonical))
	for _, canonical := range fixture.ConfiguredDeploymentIdentities.PublicModels {
		backend := backendByCanonical[canonical]
		backendSet[backend] = struct{}{}
		providerAliases[canonical] = backend
		dbModels = append(dbModels, config.ModelRPMConfig{
			Name: canonical, Credential: credential.Name,
			DeploymentID: "deployment-" + canonical, RPM: -1, TPM: -1,
		})
	}
	staticModels := make([]config.ModelRPMConfig, 0, len(backendSet))
	for backend := range backendSet {
		staticModels = append(staticModels, config.ModelRPMConfig{
			Name: backend, Credential: credential.Name, RPM: -1, TPM: -1,
		})
	}
	publicAliases := make(map[string]string, fixture.LiveAliasSurface.Count)
	for _, row := range fixture.LiveAliasSurface.Aliases {
		publicAliases[row.Alias] = row.ConfiguredPublicModel
	}
	acceptedAliases := make(map[string]string, fixture.AcceptedUnadvertisedIdentifiers.Count)
	for _, row := range fixture.AcceptedUnadvertisedIdentifiers.Identifiers {
		acceptedAliases[row.Identifier] = row.ConfiguredPublicModel
	}

	manager := New(logger, -1, staticModels)
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	manager.SetCredentials([]config.CredentialConfig{credential})
	manager.SetModelAliases(providerAliases)
	manager.SetClientModelIDs(fixture.ConfiguredDeploymentIdentities.PublicModels)
	manager.UpdateDBModels(dbModels, []config.CredentialConfig{credential}, []config.CredentialConfig{credential})
	manager.SetPublicModelAliases(publicAliases)
	manager.SetAcceptedModelAliases(acceptedAliases)

	catalog := manager.GetAllModels()
	catalogIDs := make([]string, 0, len(catalog.Data))
	for _, model := range catalog.Data {
		catalogIDs = append(catalogIDs, model.ID)
	}
	assert.ElementsMatch(t, fixture.EffectivePublicSurface.PublicModels, catalogIDs)
	assert.Len(t, catalogIDs, 144)
	accessGroupCatalog := manager.GetAllModelsWithAccessGroups()
	accessGroupIDs := make([]string, 0, len(accessGroupCatalog.Data))
	for _, model := range accessGroupCatalog.Data {
		accessGroupIDs = append(accessGroupIDs, model.ID)
	}
	assert.ElementsMatch(t, catalogIDs, accessGroupIDs)

	for _, modelID := range fixture.IngressCompatibilitySurface.Identifiers {
		assert.True(t, manager.IsClientModelIDRoutable(modelID), modelID)
	}
	ingressIDs := make(map[string]struct{}, len(fixture.IngressCompatibilitySurface.Identifiers))
	for _, modelID := range fixture.IngressCompatibilitySurface.Identifiers {
		ingressIDs[modelID] = struct{}{}
	}
	ghosts := make([]string, 0)
	for backend := range backendSet {
		if _, accepted := ingressIDs[backend]; accepted {
			continue
		}
		ghosts = append(ghosts, backend)
		assert.False(t, manager.IsClientModelIDRoutable(backend), backend)
	}
	assert.NotEmpty(t, ghosts)
	for _, row := range fixture.ExcludedRouteOnlyIdentifiers.Identifiers {
		assert.False(t, manager.IsClientModelIDRoutable(row.Identifier), row.Identifier)
	}
}

func TestAcceptedUnadvertisedIdentifiersResolveButRemainHidden(t *testing.T) {
	fixture := loadLivePublicModelSurfaceFixture(t)
	require.Equal(t, 35, fixture.AcceptedUnadvertisedIdentifiers.Count)
	require.Len(t, fixture.AcceptedUnadvertisedIdentifiers.Identifiers, 35)
	require.Equal(t, 7, fixture.AcceptedUnadvertisedIdentifiers.RecencySubsetLast24HCount)
	require.Equal(t, 179, fixture.IngressCompatibilitySurface.Count)
	require.Len(t, fixture.IngressCompatibilitySurface.Identifiers, 179)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	credential := config.CredentialConfig{Name: "provider", Type: config.ProviderTypeOpenAI}
	staticModels := make([]config.ModelRPMConfig, 0, 35)
	dbModels := make([]config.ModelRPMConfig, 0, 35)
	providerAliases := make(map[string]string, 35)
	acceptedAliases := make(map[string]string, 35)
	deploymentIDs := make(map[string]string, 35)
	for _, row := range fixture.AcceptedUnadvertisedIdentifiers.Identifiers {
		staticModels = append(staticModels, config.ModelRPMConfig{
			Name: row.BackendModel, Credential: credential.Name, RPM: -1, TPM: -1,
		})
		deploymentID := "deployment-" + row.ConfiguredPublicModel
		dbModels = append(dbModels, config.ModelRPMConfig{
			Name: row.ConfiguredPublicModel, Credential: credential.Name,
			DeploymentID: deploymentID, RPM: -1, TPM: -1,
		})
		providerAliases[row.ConfiguredPublicModel] = row.BackendModel
		acceptedAliases[row.Identifier] = row.ConfiguredPublicModel
		deploymentIDs[row.ConfiguredPublicModel] = deploymentID
	}
	manager := New(logger, -1, staticModels)
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	manager.SetCredentials([]config.CredentialConfig{credential})
	manager.SetModelAliases(providerAliases)
	manager.UpdateDBModels(dbModels, []config.CredentialConfig{credential}, []config.CredentialConfig{credential})
	manager.SetAcceptedModelAliases(acceptedAliases)

	catalog := manager.GetAllModels()
	catalogIDs := make(map[string]struct{}, len(catalog.Data))
	for _, model := range catalog.Data {
		catalogIDs[model.ID] = struct{}{}
	}
	for _, row := range fixture.AcceptedUnadvertisedIdentifiers.Identifiers {
		canonical, accepted, err := manager.ResolvePublicModelAlias(row.Identifier)
		require.NoError(t, err, row.Identifier)
		require.True(t, accepted, row.Identifier)
		assert.Equal(t, row.ConfiguredPublicModel, canonical, row.Identifier)
		backend, providerAlias := manager.ResolveAlias(canonical)
		require.True(t, providerAlias, row.Identifier)
		assert.Equal(t, row.BackendModel, backend, row.Identifier)
		deploymentID, ok := manager.GetDeploymentID(row.Identifier, credential.Name)
		require.True(t, ok, row.Identifier)
		assert.Equal(t, deploymentIDs[canonical], deploymentID, row.Identifier)
		assert.True(t, manager.IsModelIDAllowedByScope(row.Identifier, []string{canonical}), row.Identifier)
		_, published := catalogIDs[row.Identifier]
		assert.False(t, published, "accepted compatibility ID leaked into /v1/models: %s", row.Identifier)
	}
}

func loadLivePublicModelSurfaceFixture(t *testing.T) livePublicModelSurfaceFixture {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/live-public-model-surface.json")
	require.NoError(t, err)
	var fixture livePublicModelSurfaceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	return fixture
}

func TestLivePublicModelAliasesResolveToExactDeploymentAndBackend(t *testing.T) {
	fixture := loadLivePublicModelSurfaceFixture(t)
	require.Equal(t, 107, fixture.ConfiguredDeploymentIdentities.Count)
	require.Len(t, fixture.ConfiguredDeploymentIdentities.PublicModels, 107)
	require.Equal(t, 37, fixture.LiveAliasSurface.Count)
	require.Len(t, fixture.LiveAliasSurface.Aliases, 37)
	require.Equal(t, 29, fixture.LiveAliasSurface.DistinctConfiguredTargets)
	require.Equal(t, 144, fixture.EffectivePublicSurface.Count)
	require.Len(t, fixture.EffectivePublicSurface.PublicModels, 144)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	credential := config.CredentialConfig{Name: "provider", Type: config.ProviderTypeOpenAI}
	backendModels := make(map[string]struct{})
	canonicalModels := make(map[string]struct{})
	publicAliases := make(map[string]string)
	providerAliases := make(map[string]string)
	for _, row := range fixture.LiveAliasSurface.Aliases {
		require.NotEmpty(t, row.Alias)
		require.NotEmpty(t, row.ConfiguredPublicModel)
		require.NotEmpty(t, row.BackendModel)
		require.Contains(t, []string{"chat", "embedding", "image_generation"}, row.Mode)
		_, duplicate := publicAliases[row.Alias]
		require.False(t, duplicate, "duplicate live alias %q", row.Alias)
		publicAliases[row.Alias] = row.ConfiguredPublicModel
		providerAliases[row.ConfiguredPublicModel] = row.BackendModel
		backendModels[row.BackendModel] = struct{}{}
		canonicalModels[row.ConfiguredPublicModel] = struct{}{}
	}
	require.Len(t, canonicalModels, 29)

	staticModels := make([]config.ModelRPMConfig, 0, len(backendModels))
	for backend := range backendModels {
		staticModels = append(staticModels, config.ModelRPMConfig{
			Name: backend, Credential: credential.Name, RPM: -1, TPM: -1,
		})
	}
	manager := New(logger, -1, staticModels)
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	manager.SetCredentials([]config.CredentialConfig{credential})
	manager.SetModelAliases(providerAliases)

	dbModels := make([]config.ModelRPMConfig, 0, len(canonicalModels))
	deploymentIDs := make(map[string]string, len(canonicalModels))
	for publicModel := range canonicalModels {
		deploymentID := "deployment-" + publicModel
		deploymentIDs[publicModel] = deploymentID
		dbModels = append(dbModels, config.ModelRPMConfig{
			Name: publicModel, Credential: credential.Name,
			DeploymentID: deploymentID, RPM: -1, TPM: -1,
		})
	}
	manager.UpdateDBModels(dbModels, []config.CredentialConfig{credential}, []config.CredentialConfig{credential})
	manager.SetPublicModelAliases(publicAliases)

	catalog := manager.GetAllModels()
	catalogIDs := make(map[string]struct{}, len(catalog.Data))
	for _, model := range catalog.Data {
		catalogIDs[model.ID] = struct{}{}
	}
	for _, row := range fixture.LiveAliasSurface.Aliases {
		canonical, isAlias, err := manager.ResolvePublicModelAlias(row.Alias)
		require.NoError(t, err, row.Alias)
		require.True(t, isAlias, row.Alias)
		assert.Equal(t, row.ConfiguredPublicModel, canonical, row.Alias)

		backend, isProviderAlias := manager.ResolveAlias(canonical)
		require.True(t, isProviderAlias, row.Alias)
		assert.Equal(t, row.BackendModel, backend, row.Alias)

		deploymentID, ok := manager.GetDeploymentID(row.Alias, credential.Name)
		require.True(t, ok, row.Alias)
		assert.Equal(t, deploymentIDs[canonical], deploymentID, row.Alias)
		assert.True(t, manager.IsModelIDAllowedByScope(row.Alias, []string{canonical}), row.Alias)
		_, published := catalogIDs[row.Alias]
		assert.True(t, published, row.Alias)
	}
}

func TestPublicModelAliasFailsClosedForOrphanTarget(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	credential := config.CredentialConfig{Name: "provider", Type: config.ProviderTypeOpenAI}
	manager := New(logger, -1, nil)
	manager.SetCredentials([]config.CredentialConfig{credential})
	manager.SetPublicModelAliases(map[string]string{
		"alias/orphan": "canonical/missing",
	})

	resolved, configured, err := manager.ResolvePublicModelAlias("alias/orphan")
	assert.Equal(t, "alias/orphan", resolved)
	assert.True(t, configured)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"alias/orphan"`)
	_, ok := manager.GetDeploymentID("alias/orphan", credential.Name)
	assert.False(t, ok)
	assert.False(t, manager.IsModelIDAllowedByScope("alias/orphan", []string{"*"}))
}

func TestPublicModelAliasStaysActiveWithAmbiguousDeploymentAttribution(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	credential := config.CredentialConfig{Name: "provider", Type: config.ProviderTypeOpenAI}
	manager := New(logger, -1, nil)
	manager.SetCredentials([]config.CredentialConfig{credential})
	manager.UpdateDBModels([]config.ModelRPMConfig{
		{Name: "canonical/ambiguous", Credential: credential.Name, DeploymentID: "deployment-a"},
		{Name: "canonical/ambiguous", Credential: credential.Name, DeploymentID: "deployment-b"},
	}, []config.CredentialConfig{credential}, []config.CredentialConfig{credential})
	manager.SetPublicModelAliases(map[string]string{
		"alias/ambiguous": "canonical/ambiguous",
	})

	// Two LiteLLM deployments behind one public model (e.g. primary+fallback)
	// must not kill the alias: activation follows routability only.
	resolved, configured, err := manager.ResolvePublicModelAlias("alias/ambiguous")
	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, "canonical/ambiguous", resolved)
	assert.True(t, manager.IsModelIDAllowedByScope("alias/ambiguous", []string{"*"}))

	// Deployment attribution stays best-effort: ambiguity resolves to no ID.
	_, ok := manager.GetDeploymentID("alias/ambiguous", credential.Name)
	assert.False(t, ok)
}

func TestPublicModelAliasActiveWithoutDeploymentMetadata(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	credential := config.CredentialConfig{Name: "provider", Type: config.ProviderTypeOpenAI}
	manager := New(logger, -1, []config.ModelRPMConfig{{
		Name:       "openai/gpt-4o",
		Credential: credential.Name,
	}})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	manager.SetCredentials([]config.CredentialConfig{credential})
	// No UpdateDBModels: the LiteLLM model table is unavailable, so no
	// deployment IDs exist at all. The alias must still be active.
	manager.SetPublicModelAliases(map[string]string{
		"gpt-4o": "openai/gpt-4o",
	})

	resolved, configured, err := manager.ResolvePublicModelAlias("gpt-4o")
	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, "openai/gpt-4o", resolved)
	assert.True(t, manager.IsModelIDAllowedByScope("gpt-4o", []string{"gpt-4o"}))
	assert.True(t, manager.IsModelIDAllowedByScope("gpt-4o", []string{"openai/gpt-4o"}))

	catalog := manager.GetAllModels()
	catalogIDs := make(map[string]struct{}, len(catalog.Data))
	for _, model := range catalog.Data {
		catalogIDs[model.ID] = struct{}{}
	}
	_, published := catalogIDs["gpt-4o"]
	assert.True(t, published, "alias must be projected without LiteLLM deployment metadata")

	_, ok := manager.GetDeploymentID("gpt-4o", credential.Name)
	assert.False(t, ok, "no deployment attribution is available without the LiteLLM model table")
}

func TestPublicModelAliasDoesNotRevokeDirectScopeAccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	credential := config.CredentialConfig{Name: "provider", Type: config.ProviderTypeOpenAI}
	manager := New(logger, -1, []config.ModelRPMConfig{
		{Name: "gpt-4o", Credential: credential.Name},
		{Name: "openai/gpt-4o", Credential: credential.Name},
	})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	manager.SetCredentials([]config.CredentialConfig{credential})

	assert.True(t, manager.IsModelIDAllowedByScope("gpt-4o", []string{"gpt-4o"}),
		"baseline: the short name is directly allowed by the scope")

	// Registering a public alias for the same short name must not revoke the
	// already granted direct scope access.
	manager.SetPublicModelAliases(map[string]string{
		"gpt-4o": "openai/gpt-4o",
	})
	assert.True(t, manager.IsModelIDAllowedByScope("gpt-4o", []string{"gpt-4o"}),
		"adding public_model_alias must not revoke previously issued short-name access")
	assert.True(t, manager.IsModelIDAllowedByScope("gpt-4o", []string{"openai/gpt-4o"}),
		"the alias also inherits permission from its canonical target")
}
