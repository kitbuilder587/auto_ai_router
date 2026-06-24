package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/converter"
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

func TestOrchestrateRequest_CometAPIChatCompletionPreservesExplicitCacheControl(t *testing.T) {
	prx := NewTestProxyBuilder().
		WithSingleCredential("comet", config.ProviderTypeCometAPI, "https://api.cometapi.com/v1", "upstream-key").
		WithMasterKey("master-key").
		Build()

	body := `{
		"model": "claude-sonnet-4.5",
		"user": "session-1",
		"messages": [
			{"role": "system", "content": [
				{"type": "text", "text": "Stable system prompt", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "user", "content": [
				{"type": "text", "text": "Cached history", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "assistant", "content": "Previous answer"},
			{"role": "user", "content": "Current question"}
		]
	}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)
	require.Equal(t, config.ProviderTypeCometAPI, prepared.cred.Type)

	conv := converter.New(prepared.cred.Type, converter.RequestMode{
		ModelID:        prepared.realModelID,
		DisplayModelID: prepared.modelID,
		ContentType:    req.Header.Get("Content-Type"),
	})
	require.Equal(t, "https://api.cometapi.com/v1/messages", conv.BuildURL(prepared.cred))

	cometBody, err := conv.RequestFrom(prepared.body)
	require.NoError(t, err)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(cometBody, &raw))
	require.Equal(t, "claude-sonnet-4.5", raw["model"])
	require.Equal(t, map[string]interface{}{"user_id": "session-1"}, raw["metadata"])

	system := raw["system"].([]interface{})
	require.Equal(t, map[string]interface{}{"type": "ephemeral"}, system[0].(map[string]interface{})["cache_control"])

	messages := raw["messages"].([]interface{})
	firstUserContent := messages[0].(map[string]interface{})["content"].([]interface{})
	require.Equal(t, map[string]interface{}{"type": "ephemeral"}, firstUserContent[0].(map[string]interface{})["cache_control"])
}

func TestOrchestrateRequest_CometAPIChatCompletionAutoInjectsCacheControlForSession(t *testing.T) {
	prx := NewTestProxyBuilder().
		WithSingleCredential("comet", config.ProviderTypeCometAPI, "https://api.cometapi.com/v1", "upstream-key").
		WithMasterKey("master-key").
		Build()
	prx.stickyAutoCacheCtrl = true

	body := `{
		"model": "claude-sonnet-4.5",
		"user": "session-1",
		"messages": [
			{"role": "system", "content": "Stable system prompt"},
			{"role": "user", "content": "Cached history"},
			{"role": "assistant", "content": "Previous answer"},
			{"role": "user", "content": "Current question"}
		]
	}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)
	require.Equal(t, "session-1", logCtx.SessionID)

	conv := converter.New(prepared.cred.Type, converter.RequestMode{
		ModelID:        prepared.realModelID,
		DisplayModelID: prepared.modelID,
		ContentType:    req.Header.Get("Content-Type"),
	})
	cometBody, err := conv.RequestFrom(prepared.body)
	require.NoError(t, err)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(cometBody, &raw))

	system := raw["system"].([]interface{})
	require.Equal(t, map[string]interface{}{"type": "ephemeral"}, system[0].(map[string]interface{})["cache_control"])

	messages := raw["messages"].([]interface{})
	firstUserContent := messages[0].(map[string]interface{})["content"].([]interface{})
	require.Equal(t, map[string]interface{}{"type": "ephemeral"}, firstUserContent[0].(map[string]interface{})["cache_control"])

	currentUserContent := messages[2].(map[string]interface{})["content"].([]interface{})
	require.Equal(t, "Current question", currentUserContent[0].(map[string]interface{})["text"])
	require.Nil(t, currentUserContent[0].(map[string]interface{})["cache_control"])
}
