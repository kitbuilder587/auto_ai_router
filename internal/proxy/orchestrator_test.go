package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/require"
)

func TestOrchestrateRequest_ResponsesAPIStreaming(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	prx := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeAnthropic, "http://test.local", "upstream-key").
		WithMasterKey("master-key").
		Build()
	prx.logger = logger

	body := `{"model":"Xpt-5","input":"Hello","stream":true}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)

	require.True(t, prepared.isResponsesAPI)
	require.True(t, prepared.streaming)
	require.True(t, prepared.convertedResp)
	require.Equal(t, "/v1/chat/completions", prepared.request.URL.Path)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(prepared.body, &raw))

	_, hasInput := raw["input"]
	require.False(t, hasInput, "input should be removed after conversion")

	_, hasMessages := raw["messages"]
	require.True(t, hasMessages, "messages should be present after conversion")

	streamOptions, ok := raw["stream_options"].(map[string]interface{})
	require.True(t, ok, "stream_options should be present")
	require.Equal(t, true, streamOptions["include_usage"])
}

func TestOrchestrateRequest_ResponsesAPI_PassthroughForOpenAI(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	prx := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeOpenAI, "http://test.local", "upstream-key").
		WithMasterKey("master-key").
		Build()
	prx.logger = logger

	body := `{"model":"qwen-5","input":"Hello","stream":false}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)

	require.True(t, prepared.isResponsesAPI)
	require.False(t, prepared.convertedResp)
	require.True(t, prepared.passthroughResponses)
	require.Equal(t, "/v1/responses", prepared.request.URL.Path)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(prepared.body, &raw))

	_, hasInput := raw["input"]
	require.True(t, hasInput, "input should remain for native passthrough")
	_, hasMessages := raw["messages"]
	require.False(t, hasMessages, "messages should not be injected for native passthrough")
}

func TestOrchestrateRequest_ResponsesAPI_ConvertedForOpenAIWhenPassthroughDisabled(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	passthroughResponses := false
	builder := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeOpenAI, "http://test.local", "upstream-key").
		WithMasterKey("master-key")
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{
		{
			Name:                 "qwen-5",
			PassthroughResponses: &passthroughResponses,
		},
	})
	prx := builder.Build()
	prx.logger = logger

	body := `{"model":"qwen-5","input":"Hello","stream":false}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)

	require.True(t, prepared.isResponsesAPI)
	require.True(t, prepared.convertedResp)
	require.False(t, prepared.passthroughResponses)
	require.Equal(t, "/v1/chat/completions", prepared.request.URL.Path)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(prepared.body, &raw))

	_, hasInput := raw["input"]
	require.False(t, hasInput, "input should be removed after conversion")
	_, hasMessages := raw["messages"]
	require.True(t, hasMessages, "messages should be present after conversion")
}

func TestRebindBodyForCredential_UsesCredentialSpecificRealModel(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	cheap := config.CredentialConfig{Name: "cheapgpt", Type: config.ProviderTypeAnthropic, APIKey: "key", BaseURL: "http://cheapgpt.local", RPM: 100}
	grant := config.CredentialConfig{Name: "grant", Type: config.ProviderTypeBedrock, APIKey: "key2", BaseURL: "http://grant.local", RPM: 100}
	mm := models.New(logger, 50, []config.ModelRPMConfig{
		{Name: "claude", Model: "anthropic/claude-sonnet", Credential: cheap.Name},
		{Name: "claude", Model: "global.anthropic.claude-sonnet-v1:0", Credential: grant.Name},
	})
	mm.LoadModelsFromConfig([]config.CredentialConfig{cheap, grant})

	builder := NewTestProxyBuilder().WithCredentials(cheap, grant)
	builder.config.ModelManager = mm
	prx := builder.Build()

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	body := []byte(`{"model":"anthropic/claude-sonnet","messages":[]}`)

	nextBody, nextRealModel := prx.rebindBodyForCredential(req, body, "claude", "anthropic/claude-sonnet", &grant)

	require.Equal(t, "global.anthropic.claude-sonnet-v1:0", nextRealModel)
	require.Contains(t, string(nextBody), `"model":"global.anthropic.claude-sonnet-v1:0"`)
}
