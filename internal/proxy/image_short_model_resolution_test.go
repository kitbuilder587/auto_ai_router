package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	dbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyRequestResolvesLiteLLMStrippedImageBackendNames(t *testing.T) {
	tests := []struct {
		publicModel  string
		backendModel string
		shortModel   string
	}{
		{
			publicModel:  "google/gemini-2.5-flash-image",
			backendModel: "openai/gemini-2.5-flash-image",
			shortModel:   "gemini-2.5-flash-image",
		},
		{
			publicModel:  "google/gemini-3-pro-image-preview",
			backendModel: "openai/gemini-3-pro-image-preview",
			shortModel:   "gemini-3-pro-image-preview",
		},
		{
			publicModel:  "google/gemini-3.1-flash-image-preview",
			backendModel: "openai/gemini-3.1-flash-image-preview",
			shortModel:   "gemini-3.1-flash-image-preview",
		},
		{
			publicModel:  "vertex_ai/imagen-4.0-fast-generate-001",
			backendModel: "openai/imagen-4.0-fast-generate-001",
			shortModel:   "imagen-4.0-fast-generate-001",
		},
		{
			publicModel:  "vertex_ai/imagen-4.0-generate-001",
			backendModel: "openai/imagen-4.0-generate-001",
			shortModel:   "imagen-4.0-generate-001",
		},
		{
			publicModel:  "vertex_ai/imagen-4.0-ultra-generate-001",
			backendModel: "openai/imagen-4.0-ultra-generate-001",
			shortModel:   "imagen-4.0-ultra-generate-001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.publicModel, func(t *testing.T) {
			var receivedModel string
			upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				receivedModel, _ = body["model"].(string)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"aW1hZ2U="}]}`))
			}))
			defer upstream.Close()

			const credentialName = "mock-openai"
			credential := config.CredentialConfig{
				Name:    credentialName,
				Type:    config.ProviderTypeOpenAI,
				BaseURL: upstream.URL,
				APIKey:  "upstream-key",
				RPM:     100,
				TPM:     1000,
			}
			manager := models.New(testhelpers.NewTestLogger(), 100, []config.ModelRPMConfig{
				{Name: tt.backendModel, Credential: credentialName, RPM: 100, TPM: 1000},
			})
			manager.SetModelAliases(map[string]string{tt.publicModel: tt.backendModel})
			manager.LoadModelsFromConfig([]config.CredentialConfig{credential})

			builder := NewTestProxyBuilder().
				WithCredentials(credential).
				WithMasterKey("master-key")
			builder.config.ModelManager = manager
			prx := builder.Build()

			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
				`{"model":"`+tt.shortModel+`","prompt":"catalog-image"}`,
			))
			req.Header.Set("Authorization", "Bearer master-key")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			prx.ProxyRequest(w, req)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, tt.backendModel, receivedModel)
		})
	}
}

func TestImageShortNameResolutionDoesNotBypassClientModelACL(t *testing.T) {
	const (
		credentialName = "mock-openai"
		publicModel    = "google/gemini-2.5-flash-image"
		backendModel   = "openai/gemini-2.5-flash-image"
		shortModel     = "gemini-2.5-flash-image"
	)
	credential := config.CredentialConfig{Name: credentialName, Type: config.ProviderTypeOpenAI}
	manager := models.New(testhelpers.NewTestLogger(), 100, []config.ModelRPMConfig{
		{Name: backendModel, Credential: credentialName},
	})
	manager.SetModelAliases(map[string]string{publicModel: backendModel})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	builder := NewTestProxyBuilder().WithCredentials(credential)
	builder.config.ModelManager = manager
	prx := builder.Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
		`{"model":"`+shortModel+`","prompt":"catalog-image"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{
		Request:   req,
		TokenInfo: &dbmodels.TokenInfo{Models: []string{publicModel}},
	}

	_, _, _, _, ok := prx.readRequestBodyAndSelectModel(w, req, logCtx)

	assert.False(t, ok)
	testhelpers.AssertJSONErrorResponse(t, w, http.StatusForbidden, "permission_denied", "Model not allowed")
	assert.Equal(t, shortModel, logCtx.PublicModelID)
	assert.Equal(t, shortModel, logCtx.ModelID, "ACL must reject the original short name before resolution")
}

func TestImageShortNameCollisionReturnsNotFound(t *testing.T) {
	const (
		credentialName = "mock-openai"
		shortModel     = "shared-image"
	)
	credential := config.CredentialConfig{
		Name:    credentialName,
		Type:    config.ProviderTypeOpenAI,
		BaseURL: "http://provider.invalid",
		APIKey:  "upstream-key",
		RPM:     100,
		TPM:     1000,
	}
	manager := models.New(testhelpers.NewTestLogger(), 100, []config.ModelRPMConfig{
		{Name: "openai/shared-image", Credential: credentialName},
		{Name: "vertex_ai/shared-image", Credential: credentialName},
	})
	manager.SetModelAliases(map[string]string{
		"public/openai-image": "openai/shared-image",
		"public/vertex-image": "vertex_ai/shared-image",
	})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	builder := NewTestProxyBuilder().
		WithCredentials(credential).
		WithMasterKey("master-key")
	builder.config.ModelManager = manager
	prx := builder.Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(
		`{"model":"`+shortModel+`","prompt":"catalog-image"}`,
	))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	testhelpers.AssertJSONErrorResponse(t, w, http.StatusNotFound, "not_found_error", "Model shared-image not found")
}

func TestShortNameFallbackIsLimitedToImageGeneration(t *testing.T) {
	const (
		credentialName = "mock-openai"
		publicModel    = "google/gemini-2.5-flash-image"
		backendModel   = "openai/gemini-2.5-flash-image"
		shortModel     = "gemini-2.5-flash-image"
	)
	credential := config.CredentialConfig{Name: credentialName, Type: config.ProviderTypeOpenAI}
	manager := models.New(testhelpers.NewTestLogger(), 100, []config.ModelRPMConfig{
		{Name: backendModel, Credential: credentialName},
	})
	manager.SetModelAliases(map[string]string{publicModel: backendModel})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	builder := NewTestProxyBuilder().WithCredentials(credential)
	builder.config.ModelManager = manager
	prx := builder.Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"`+shortModel+`","messages":[{"role":"user","content":"hello"}]}`,
	))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{Request: req}

	_, modelID, realModelID, _, ok := prx.readRequestBodyAndSelectModel(w, req, logCtx)

	require.True(t, ok)
	assert.Equal(t, shortModel, modelID)
	assert.Equal(t, shortModel, realModelID)
}
