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

func TestOrchestrateRequest_RejectsImageGenerationModelOnChatCompletions(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	builder := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeProxy, "http://test.local", "upstream-key").
		WithMasterKey("master-key")
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{
		{
			Name:  "openai/gpt-image-1",
			Model: "gpt-image-1",
			Mode:  config.ModelModeImageGeneration,
		},
	})
	prx := builder.Build()
	prx.logger = logger

	body := `{"model":"openai/gpt-image-1","messages":[{"role":"user","content":"draw"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.False(t, ok)
	require.Nil(t, prepared)
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "/v1/images/generations")
	require.Equal(t, "failure", logCtx.Status)
	require.Equal(t, 400, logCtx.HTTPStatus)
}

func TestOrchestrateRequest_AllowsImageGenerationModelOnImagesEndpoint(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	builder := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeProxy, "http://test.local", "upstream-key").
		WithMasterKey("master-key")
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{
		{
			Name:  "openai/gpt-image-1",
			Model: "gpt-image-1",
			Mode:  config.ModelModeImageGeneration,
		},
	})
	prx := builder.Build()
	prx.logger = logger

	body := `{"model":"openai/gpt-image-1","prompt":"draw"}`
	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)
	require.Equal(t, "openai/gpt-image-1", prepared.modelID)
	require.Equal(t, "gpt-image-1", prepared.realModelID)
	require.Equal(t, 200, w.Code)
}

func TestOrchestrateRequest_AllowsChatImageGenerationModelOnTextChat(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	builder := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeProxy, "http://test.local", "upstream-key").
		WithMasterKey("master-key")
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{
		{
			Name:  "google/gemini-3-pro-image-preview",
			Model: "gemini-3-pro-image-preview",
			Mode:  config.ModelModeChatImageGeneration,
		},
	})
	prx := builder.Build()
	prx.logger = logger

	body := `{"model":"google/gemini-3-pro-image-preview","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)
	require.Equal(t, "google/gemini-3-pro-image-preview", prepared.modelID)
	require.Equal(t, "gemini-3-pro-image-preview", prepared.realModelID)
	require.Equal(t, 200, w.Code)
}

func TestOrchestrateRequest_RejectsChatImageGenerationModelImageOutputOnChat(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	builder := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeProxy, "http://test.local", "upstream-key").
		WithMasterKey("master-key")
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{
		{
			Name:  "google/gemini-3-pro-image-preview",
			Model: "gemini-3-pro-image-preview",
			Mode:  config.ModelModeChatImageGeneration,
		},
	})
	prx := builder.Build()
	prx.logger = logger

	body := `{"model":"google/gemini-3-pro-image-preview","messages":[{"role":"user","content":"draw"}],"modalities":["image","text"]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.False(t, ok)
	require.Nil(t, prepared)
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "/v1/images/generations")
	require.Contains(t, w.Body.String(), "/v1/responses")
	require.Equal(t, "failure", logCtx.Status)
	require.Equal(t, 400, logCtx.HTTPStatus)
}

func TestOrchestrateRequest_AllowsChatImageGenerationModelOnResponses(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	builder := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeProxy, "http://test.local", "upstream-key").
		WithMasterKey("master-key")
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{
		{
			Name:  "google/gemini-3-pro-image-preview",
			Model: "gemini-3-pro-image-preview",
			Mode:  config.ModelModeChatImageGeneration,
		},
	})
	prx := builder.Build()
	prx.logger = logger

	body := `{"model":"google/gemini-3-pro-image-preview","input":"draw","tools":[{"type":"image_generation"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)
	require.Equal(t, "google/gemini-3-pro-image-preview", prepared.modelID)
	require.Equal(t, "gemini-3-pro-image-preview", prepared.realModelID)
	require.Equal(t, 200, w.Code)
}

func TestOrchestrateRequest_RejectsEmbeddingModelOnChatCompletions(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	builder := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeProxy, "http://test.local", "upstream-key").
		WithMasterKey("master-key")
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{
		{
			Name:  "openai/text-embedding-3-small",
			Model: "text-embedding-3-small",
			Mode:  config.ModelModeEmbeddingGeneration,
		},
	})
	prx := builder.Build()
	prx.logger = logger

	body := `{"model":"openai/text-embedding-3-small","messages":[{"role":"user","content":"embed"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.False(t, ok)
	require.Nil(t, prepared)
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "/v1/embeddings")
	require.Equal(t, "failure", logCtx.Status)
	require.Equal(t, 400, logCtx.HTTPStatus)
}

func TestOrchestrateRequest_AllowsEmbeddingModelOnEmbeddingsEndpoint(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	builder := NewTestProxyBuilder().
		WithSingleCredential("test", config.ProviderTypeProxy, "http://test.local", "upstream-key").
		WithMasterKey("master-key")
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{
		{
			Name:  "openai/text-embedding-3-small",
			Model: "text-embedding-3-small",
			Mode:  config.ModelModeEmbeddingGeneration,
		},
	})
	prx := builder.Build()
	prx.logger = logger

	body := `{"model":"openai/text-embedding-3-small","input":"hello"}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)
	require.True(t, ok)
	require.NotNil(t, prepared)
	require.Equal(t, "openai/text-embedding-3-small", prepared.modelID)
	require.Equal(t, "text-embedding-3-small", prepared.realModelID)
	require.Equal(t, 200, w.Code)
}
