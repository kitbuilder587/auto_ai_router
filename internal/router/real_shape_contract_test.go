package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	dbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealShapeAuthenticatedModelsRemainKeyScopedWithoutUsingMockCountAsCatalogOracle(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/air-migration/real-shape-constraints-v2.json")
	require.NoError(t, err)
	var constraints struct {
		SchemaVersion  string `json:"schema_version"`
		WireMockShapes struct {
			AuthenticatedModelListCount int `json:"authenticated_model_list_count"`
		} `json:"wiremock_shapes"`
		MigrationGate struct {
			BlockingOperations []string `json:"blocking_operations"`
		} `json:"migration_gate"`
	}
	require.NoError(t, json.Unmarshal(raw, &constraints))
	require.Equal(t, "air-migration-real-shape-constraints/v2", constraints.SchemaVersion)
	require.Contains(t, constraints.MigrationGate.BlockingOperations, "GET /v1/models")
	require.Positive(t, constraints.WireMockShapes.AuthenticatedModelListCount)

	// The WireMock count describes its own canned response, not AIR's configured
	// catalog. Use a deliberately different synthetic size and apply only the
	// portable contract: authenticated list schema plus virtual-key filtering.
	const syntheticCatalogSize = 3
	require.NotEqual(t, constraints.WireMockShapes.AuthenticatedModelListCount, syntheticCatalogSize)
	staticModels := make([]config.ModelRPMConfig, 0, syntheticCatalogSize)
	allIDs := make([]string, 0, syntheticCatalogSize)
	for index := range syntheticCatalogSize {
		id := fmt.Sprintf("fixture/model-%02d", index)
		allIDs = append(allIDs, id)
		staticModels = append(staticModels, config.ModelRPMConfig{Name: id, RPM: 100, TPM: 10000})
	}
	manager := models.New(testhelpers.NewTestLogger(), 100, staticModels)
	credentials := []config.CredentialConfig{{Name: "fixture-catalog", Type: config.ProviderTypeOpenAI}}
	manager.SetCredentials(credentials)
	manager.LoadModelsFromConfig(credentials)

	prx := createTestProxy()
	scopedIDs := []string{allIDs[0], allIDs[len(allIDs)-1]}
	prx.LiteLLMDB = &routerAuthTestDB{tokens: map[string]*dbmodels.TokenInfo{
		"fixture-unrestricted-key": {Token: "fixture-unrestricted-hash"},
		"fixture-scoped-key": {
			Token:  "fixture-scoped-hash",
			Models: scopedIDs,
		},
	}}
	router := New(prx, manager, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	request := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	modelIDs := func(recorder *httptest.ResponseRecorder) []string {
		var response models.ModelsResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		require.Equal(t, "list", response.Object)
		ids := make([]string, 0, len(response.Data))
		for _, model := range response.Data {
			require.Equal(t, "model", model.Object)
			ids = append(ids, model.ID)
		}
		return ids
	}

	missing := request("")
	assert.Equal(t, http.StatusUnauthorized, missing.Code)

	unrestricted := request("fixture-unrestricted-key")
	require.Equal(t, http.StatusOK, unrestricted.Code)
	assert.Equal(t, "application/json", unrestricted.Header().Get("Content-Type"))
	assert.Equal(t, allIDs, modelIDs(unrestricted))

	scoped := request("fixture-scoped-key")
	require.Equal(t, http.StatusOK, scoped.Code)
	assert.Equal(t, "application/json", scoped.Header().Get("Content-Type"))
	assert.Equal(t, scopedIDs, modelIDs(scoped))
}
