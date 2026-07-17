package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSuccessfulChatCompletionResponse(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"model":"provider-real-model",
		"choices":[
			{"index":0,"logprobs":null,"message":{"role":"assistant","content":"ok","refusal":"blocked","provider_specific_fields":{"trace_id":"trace-1"}}},
			{"index":1,"logprobs":{"content":[]},"message":{"role":"assistant","content":"ok","refusal":null}}
		]
	}`)

	normalized := normalizeSuccessfulChatCompletionResponse(body, "public/model-alias")
	// The transform must be safe to apply more than once.
	normalized = normalizeSuccessfulChatCompletionResponse(normalized, "public/model-alias")

	var response map[string]interface{}
	require.NoError(t, json.Unmarshal(normalized, &response))
	assert.Equal(t, "public/model-alias", response["model"])

	choices := response["choices"].([]interface{})
	first := choices[0].(map[string]interface{})
	assert.NotContains(t, first, "logprobs")
	firstMessage := first["message"].(map[string]interface{})
	assert.NotContains(t, firstMessage, "refusal")
	assert.Equal(t, map[string]interface{}{
		"refusal":  "blocked",
		"trace_id": "trace-1",
	}, firstMessage["provider_specific_fields"])

	second := choices[1].(map[string]interface{})
	assert.Contains(t, second, "logprobs")
	assert.Nil(t, second["message"].(map[string]interface{})["refusal"])
}

func TestNormalizeSuccessfulChatCompletionResponsePreservesConflictingProviderRefusal(t *testing.T) {
	body := []byte(`{"object":"chat.completion","model":"real","choices":[{"message":{"refusal":"source","provider_specific_fields":{"refusal":"existing"}}}]}`)
	normalized := normalizeSuccessfulChatCompletionResponse(body, "alias")

	var response map[string]interface{}
	require.NoError(t, json.Unmarshal(normalized, &response))
	message := response["choices"].([]interface{})[0].(map[string]interface{})["message"].(map[string]interface{})
	assert.Equal(t, "source", message["refusal"])
	assert.Equal(t, "existing", message["provider_specific_fields"].(map[string]interface{})["refusal"])
}

func TestProxyRequestNormalizesDirectOpenAIChatResponse(t *testing.T) {
	const publicModel = "openai/public-model"
	const providerModel = "provider-real-model"

	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"provider-real-model","choices":[{"index":0,"logprobs":null,"message":{"role":"assistant","content":"ok","refusal":"blocked"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	builder := NewTestProxyBuilder().
		WithSingleCredential("upstream", config.ProviderTypeOpenAI, upstream.URL, "upstream-key").
		WithMasterKey("master-key")
	modelManager := models.New(testhelpers.NewTestLogger(), 50, []config.ModelRPMConfig{
		{Name: publicModel, Model: providerModel},
	})
	modelManager.LoadModelsFromConfig(builder.config.Credentials)
	builder.config.ModelManager = modelManager
	prx := builder.Build()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai/public-model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Equal(t, publicModel, response["model"])
	choice := response["choices"].([]interface{})[0].(map[string]interface{})
	assert.NotContains(t, choice, "logprobs")
	message := choice["message"].(map[string]interface{})
	assert.NotContains(t, message, "refusal")
	assert.Equal(t, "blocked", message["provider_specific_fields"].(map[string]interface{})["refusal"])
}
