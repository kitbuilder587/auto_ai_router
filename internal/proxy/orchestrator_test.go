package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	dbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/assert"
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

func TestOrchestrateRequestAppliesLiteLLMFinancialHeaderOwnership(t *testing.T) {
	tests := []struct {
		name          string
		state         shadowcontext.State
		wantFinancial bool
	}{
		{name: "direct request", state: shadowcontext.StateMissing, wantFinancial: true},
		{name: "invalid context is not trusted nesting", state: shadowcontext.StateInvalid, wantFinancial: true},
		{name: "valid LiteLLM nesting", state: shadowcontext.StateValid, wantFinancial: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prx := NewTestProxyBuilder().
				WithSingleCredential("test", config.ProviderTypeOpenAI, "http://test.local", "upstream-key").
				WithMasterKey("master-key").
				Build()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
				`{"model":"qwen-5","messages":[{"role":"user","content":"hello"}]}`,
			))
			req.Header.Set("Authorization", "Bearer master-key")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			w.Header().Set(shadowcontext.CallIDHeader, "call-1")
			if !tt.wantFinancial {
				w.Header().Set(liteLLMKeySpendHeader, "stale")
				w.Header().Set(liteLLMResponseCostHeader, "stale")
			}
			logCtx := &RequestLogContext{
				Request:       req,
				ShadowContext: shadowcontext.Result{State: tt.state},
			}

			prepared, ok := prx.orchestrateRequest(w, req, logCtx)

			require.True(t, ok)
			require.NotNil(t, prepared)
			assert.Equal(t, "call-1", w.Header().Get(shadowcontext.CallIDHeader))
			if tt.wantFinancial {
				assert.Equal(t, "0", w.Header().Get(liteLLMResponseCostHeader))
			} else {
				assert.Empty(t, w.Header().Get(liteLLMKeySpendHeader))
				assert.Empty(t, w.Header().Get(liteLLMResponseCostHeader))
			}
		})
	}
}

func TestReadRequestBodyCapturesPublicModelBeforeGlobalAliasResolution(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	manager := models.New(logger, 50, nil)
	manager.SetModelAliases(map[string]string{
		"openai/gpt-4o-mini": "backend-gpt-4o-mini",
	})
	builder := NewTestProxyBuilder().WithMasterKey("master-key")
	builder.config.ModelManager = manager
	prx := builder.Build()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`,
	))
	req.Header.Set("Content-Type", "application/json")
	logCtx := &RequestLogContext{
		RequestID: "event-direct",
		CallID:    "call-direct",
		Request:   req,
	}
	logCtx.Billing = NewBillingContext(logCtx.RequestID, logCtx.CallID, req.URL.Path, shadowcontext.Identity{})

	body, modelID, realModelID, streaming, ok := prx.readRequestBodyAndSelectModel(httptest.NewRecorder(), req, logCtx)

	require.True(t, ok)
	assert.False(t, streaming)
	assert.Equal(t, "backend-gpt-4o-mini", modelID)
	assert.Equal(t, "backend-gpt-4o-mini", realModelID)
	assert.Equal(t, "openai/gpt-4o-mini", logCtx.PublicModelID)
	assert.Equal(t, "openai/gpt-4o-mini", logCtx.Billing.PublicModel())
	assert.JSONEq(t,
		`{"model":"backend-gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`,
		string(body),
	)
}

func TestOrchestrateRequestChainsRouterAliasThroughPublicModelToProviderModel(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	credential := config.CredentialConfig{
		Name:    "db-model-gpt-4o",
		Type:    config.ProviderTypeOpenAI,
		BaseURL: "http://test.local",
		APIKey:  "upstream-key",
		RPM:     100,
		TPM:     10000,
	}
	manager := models.New(logger, 100, []config.ModelRPMConfig{{
		Name:       "openai/gpt-4o",
		Model:      "gpt-4o",
		Credential: credential.Name,
		RPM:        100,
		TPM:        10000,
	}})
	manager.SetModelAliases(map[string]string{
		"chatgpt-4o-latest": "openai/gpt-4o",
		"openai/gpt-4o":     "gpt-4o",
	})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	builder := NewTestProxyBuilder().
		WithCredentials(credential).
		WithMasterKey("master-key")
	builder.config.ModelManager = manager
	prx := builder.Build()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"chatgpt-4o-latest","messages":[{"role":"user","content":"hello"}]}`,
	))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	logCtx := &RequestLogContext{Request: req}
	logCtx.Billing = NewBillingContext("event-alias", "call-alias", req.URL.Path, shadowcontext.Identity{})

	prepared, ok := prx.orchestrateRequest(httptest.NewRecorder(), req, logCtx)

	require.True(t, ok)
	require.NotNil(t, prepared)
	assert.Equal(t, "chatgpt-4o-latest", logCtx.PublicModelID)
	assert.Equal(t, "chatgpt-4o-latest", logCtx.Billing.PublicModel())
	assert.Equal(t, "openai/gpt-4o", prepared.modelID)
	assert.Equal(t, "gpt-4o", prepared.realModelID)
	assert.Equal(t, credential.Name, prepared.cred.Name)
	assert.JSONEq(t,
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`,
		string(prepared.body),
	)
}

func TestOrchestrateRequestResolvesPublicAliasToCanonicalDeploymentAndBackend(t *testing.T) {
	const backendModel = "provider-gpt-4.1"
	logger := testhelpers.NewTestLogger()
	credential := config.CredentialConfig{
		Name: "provider", Type: config.ProviderTypeOpenAI,
		BaseURL: "http://test.local", APIKey: "upstream-key", RPM: 100, TPM: 10000,
	}
	manager := models.New(logger, 100, []config.ModelRPMConfig{
		{Name: backendModel, Credential: credential.Name, RPM: 100, TPM: 10000},
	})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	manager.SetCredentials([]config.CredentialConfig{credential})
	manager.SetModelAliases(map[string]string{"openai/gpt-4.1": backendModel})
	manager.UpdateDBModels([]config.ModelRPMConfig{{
		Name: "openai/gpt-4.1", Credential: credential.Name,
		DeploymentID: "deployment-gpt-4.1", RPM: 100, TPM: 10000,
	}}, []config.CredentialConfig{credential}, []config.CredentialConfig{credential})
	manager.SetPublicModelAliases(map[string]string{"gpt-4.1": "openai/gpt-4.1"})

	builder := NewTestProxyBuilder().WithCredentials(credential).WithMasterKey("master-key")
	builder.config.ModelManager = manager
	prx := builder.Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-4.1","messages":[{"role":"user","content":"hello"}]}`,
	))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	logCtx := &RequestLogContext{Request: req}
	logCtx.Billing = NewBillingContext("event-public-alias", "call-public-alias", req.URL.Path, shadowcontext.Identity{})

	prepared, ok := prx.orchestrateRequest(httptest.NewRecorder(), req, logCtx)

	require.True(t, ok)
	require.NotNil(t, prepared)
	assert.Equal(t, "gpt-4.1", logCtx.PublicModelID)
	assert.Equal(t, "gpt-4.1", logCtx.Billing.PublicModel())
	assert.Equal(t, backendModel, prepared.modelID)
	assert.Equal(t, backendModel, prepared.realModelID)
	assert.Equal(t, credential.Name, prepared.cred.Name)
	deploymentID, found := manager.GetDeploymentID(logCtx.PublicModelID, credential.Name)
	require.True(t, found)
	assert.Equal(t, "deployment-gpt-4.1", deploymentID)
	assert.JSONEq(t,
		`{"model":"`+backendModel+`","messages":[{"role":"user","content":"hello"}]}`,
		string(prepared.body),
	)
}

func TestOrchestrateRequestResolvesAcceptedCompletionAliasWithoutChangingPublicSemantics(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	credential := config.CredentialConfig{
		Name: "provider", Type: config.ProviderTypeOpenAI,
		APIKey: "provider-key", BaseURL: "http://provider.local", RPM: 100, TPM: 10000,
	}
	manager := models.New(logger, 100, []config.ModelRPMConfig{{
		Name: "deepseek-v4-flash", Credential: credential.Name, RPM: 100, TPM: 10000,
	}})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	manager.SetCredentials([]config.CredentialConfig{credential})
	manager.SetModelAliases(map[string]string{
		"deepseek/deepseek-v4-flash": "deepseek-v4-flash",
	})
	manager.UpdateDBModels([]config.ModelRPMConfig{{
		Name: "deepseek/deepseek-v4-flash", Credential: credential.Name,
		DeploymentID: "deployment-deepseek-v4-flash", RPM: 100, TPM: 10000,
	}}, []config.CredentialConfig{credential}, []config.CredentialConfig{credential})
	manager.SetAcceptedModelAliases(map[string]string{
		"deepseek-v4-flash": "deepseek/deepseek-v4-flash",
	})

	builder := NewTestProxyBuilder().WithCredentials(credential).WithMasterKey("master-key")
	builder.config.ModelManager = manager
	prx := builder.Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(
		`{"model":"deepseek-v4-flash","prompt":"hello","stream":false}`,
	))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	logCtx := &RequestLogContext{Request: req}
	logCtx.Billing = NewBillingContext(
		"event-accepted-completion", "call-accepted-completion", req.URL.Path,
		shadowcontext.Identity{},
	)

	prepared, ok := prx.orchestrateRequest(httptest.NewRecorder(), req, logCtx)

	require.True(t, ok)
	require.NotNil(t, prepared)
	assert.Equal(t, "/v1/completions", prepared.request.URL.Path)
	assert.Equal(t, "deepseek-v4-flash", logCtx.PublicModelID)
	assert.Equal(t, "deepseek-v4-flash", logCtx.Billing.PublicModel())
	assert.Equal(t, "deepseek-v4-flash", prepared.modelID)
	assert.Equal(t, "deepseek-v4-flash", prepared.realModelID)
	deploymentID, found := manager.GetDeploymentID(logCtx.PublicModelID, credential.Name)
	require.True(t, found)
	assert.Equal(t, "deployment-deepseek-v4-flash", deploymentID)
	assert.JSONEq(t,
		`{"model":"deepseek-v4-flash","prompt":"hello","stream":false}`,
		string(prepared.body),
	)
}

func TestOrchestrateRequestTrustedBackendWinsAcceptedAliasCollision(t *testing.T) {
	const (
		canonicalModel = "anthropic/claude-sonnet-4.5"
		backendModel   = "claude-sonnet-4.5"
	)
	logger := testhelpers.NewTestLogger()
	credential := config.CredentialConfig{
		Name: "provider", Type: config.ProviderTypeOpenAI,
		APIKey: "provider-key", BaseURL: "http://provider.local", RPM: 100, TPM: 10000,
	}
	manager := models.New(logger, 100, []config.ModelRPMConfig{{
		Name: backendModel, Credential: credential.Name, RPM: 100, TPM: 10000,
	}})
	manager.LoadModelsFromConfig([]config.CredentialConfig{credential})
	manager.SetCredentials([]config.CredentialConfig{credential})
	manager.SetModelAliases(map[string]string{canonicalModel: backendModel})
	manager.SetClientModelIDs([]string{canonicalModel})
	manager.UpdateDBModels([]config.ModelRPMConfig{
		{Name: canonicalModel, Credential: credential.Name, DeploymentID: "deployment-a", RPM: 100, TPM: 10000},
		{Name: canonicalModel, Credential: credential.Name, DeploymentID: "deployment-b", RPM: 100, TPM: 10000},
	}, []config.CredentialConfig{credential}, []config.CredentialConfig{credential})
	manager.SetAcceptedModelAliases(map[string]string{backendModel: canonicalModel})
	resolved, accepted, aliasErr := manager.ResolvePublicModelAlias(backendModel)
	assert.Equal(t, backendModel, resolved)
	assert.True(t, accepted)
	require.Error(t, aliasErr, "the collision must fail if it enters the client alias resolver")
	assert.NotEmpty(t, manager.GetCredentialsForModel(backendModel), "the same ID is an exact configured backend")

	builder := NewTestProxyBuilder().WithCredentials(credential).WithMasterKey("master-key")
	builder.config.ModelManager = manager
	prx := builder.Build()

	for _, modelID := range []string{backendModel, canonicalModel} {
		t.Run(modelID, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
				`{"model":"`+modelID+`","messages":[{"role":"user","content":"hello"}]}`,
			))
			req.Header.Set("Authorization", "Bearer master-key")
			req.Header.Set("Content-Type", "application/json")
			logCtx := &RequestLogContext{Request: req}
			logCtx.Billing = NewBillingContext("event-"+modelID, "call-"+modelID, req.URL.Path, shadowcontext.Identity{})

			prepared, ok := prx.orchestrateRequest(httptest.NewRecorder(), req, logCtx)

			require.True(t, ok)
			require.NotNil(t, prepared)
			assert.Equal(t, modelID, logCtx.PublicModelID)
			assert.Equal(t, backendModel, prepared.modelID)
			assert.Equal(t, backendModel, prepared.realModelID)
			assert.Equal(t, credential.Name, prepared.cred.Name)
			assert.JSONEq(t,
				`{"model":"`+backendModel+`","messages":[{"role":"user","content":"hello"}]}`,
				string(prepared.body),
			)
		})
	}

	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"`+backendModel+`","messages":[{"role":"user","content":"hello"}]}`,
	))
	clientReq.Header.Set("Authorization", "Bearer unrestricted-client-key")
	clientReq.Header.Set("Content-Type", "application/json")
	clientWriter := httptest.NewRecorder()
	prx.LiteLLMDB = &clientAuthTestDB{tokens: map[string]*dbmodels.TokenInfo{
		"unrestricted-client-key": {Token: "unrestricted-client-key-hash"},
	}}
	clientLogCtx := &RequestLogContext{Request: clientReq}
	clientLogCtx.Billing = NewBillingContext(
		"event-unrestricted-client", "call-unrestricted-client", clientReq.URL.Path, shadowcontext.Identity{},
	)

	prepared, ok := prx.orchestrateRequest(clientWriter, clientReq, clientLogCtx)

	assert.False(t, ok)
	assert.Nil(t, prepared)
	testhelpers.AssertJSONErrorResponse(t, clientWriter, http.StatusNotFound, "not_found_error", "Model "+backendModel+" not found")
}

func TestOrchestrateRequestRejectsOrphanPublicAliasBeforeProviderSelection(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	manager := models.New(logger, 100, nil)
	manager.SetPublicModelAliases(map[string]string{"orphan-alias": "missing/public"})
	builder := NewTestProxyBuilder().WithMasterKey("master-key")
	builder.config.ModelManager = manager
	prx := builder.Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"orphan-alias","messages":[{"role":"user","content":"hello"}]}`,
	))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{Request: req}

	prepared, ok := prx.orchestrateRequest(w, req, logCtx)

	assert.False(t, ok)
	assert.Nil(t, prepared)
	testhelpers.AssertJSONErrorResponse(t, w, http.StatusNotFound, "not_found_error", "Model orphan-alias not found")
	assert.True(t, logCtx.Logged, "pre-routing alias failures must not create zero-spend rows")
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
	builder.config.ModelManager.LoadModelsFromConfig(builder.config.Credentials)
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

func TestPrepareRequestForCredential_UsesCredentialSpecificRealModel(t *testing.T) {
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
	body := []byte(`{"model":"claude","messages":[]}`)

	prepared, err := prx.prepareRequestForCredential(
		req,
		body,
		body,
		"claude",
		"claude",
		"/v1/chat/completions",
		false,
		&grant,
		false,
		false,
		false,
	)

	require.NoError(t, err)
	require.Equal(t, "global.anthropic.claude-sonnet-v1:0", prepared.realModelID)
	require.Contains(t, string(prepared.body), `"model":"global.anthropic.claude-sonnet-v1:0"`)
}

func TestPrepareRequestForCredential_ProxyBodyKeepsOriginalParams(t *testing.T) {
	prx := NewTestProxyBuilder().Build()
	cred := config.CredentialConfig{Name: "openai", Type: config.ProviderTypeOpenAI, APIKey: "key", BaseURL: "http://openai.local", RPM: 100}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	body := []byte(`{"model":"o3-mini","messages":[],"max_tokens":100,"temperature":0.7}`)
	proxyBody := []byte(`{"model":"gpt-alias","messages":[],"max_tokens":100,"temperature":0.7}`)

	prepared, err := prx.prepareRequestForCredential(
		req,
		body,
		proxyBody,
		"gpt-alias",
		"o3-mini",
		"/v1/chat/completions",
		false,
		&cred,
		false,
		false,
		false,
	)

	require.NoError(t, err)

	var direct map[string]interface{}
	require.NoError(t, json.Unmarshal(prepared.body, &direct))
	require.Equal(t, "o3-mini", direct["model"])
	require.Contains(t, direct, "max_completion_tokens")
	require.NotContains(t, direct, "max_tokens")

	var forwarded map[string]interface{}
	require.NoError(t, json.Unmarshal(prepared.proxyBody, &forwarded))
	require.Equal(t, "gpt-alias", forwarded["model"])
	require.Contains(t, forwarded, "max_tokens")
	require.NotContains(t, forwarded, "max_completion_tokens")
	require.Contains(t, forwarded, "temperature")
}

func TestPrepareRequestForCredential_ResponsesRecomputesProviderMode(t *testing.T) {
	logger := testhelpers.NewTestLogger()
	openaiCred := config.CredentialConfig{Name: "openai", Type: config.ProviderTypeOpenAI, APIKey: "key", BaseURL: "http://openai.local", RPM: 100}
	anthropicCred := config.CredentialConfig{Name: "anthropic", Type: config.ProviderTypeAnthropic, APIKey: "key2", BaseURL: "http://anthropic.local", RPM: 100}

	builder := NewTestProxyBuilder().WithCredentials(openaiCred, anthropicCred)
	builder.config.ModelManager = models.New(logger, 50, []config.ModelRPMConfig{})
	prx := builder.Build()

	req := httptest.NewRequest("POST", "/v1/responses", nil)
	body := []byte(`{"model":"qwen-5","input":"Hello","stream":false}`)

	openaiReq, err := prx.prepareRequestForCredential(
		req,
		body,
		body,
		"qwen-5",
		"qwen-5",
		"/v1/responses",
		false,
		&openaiCred,
		true,
		false,
		false,
	)
	require.NoError(t, err)
	require.True(t, openaiReq.passthroughResponses)
	require.False(t, openaiReq.convertedResp)
	require.Equal(t, "/v1/responses", openaiReq.path)
	require.Contains(t, string(openaiReq.body), `"input"`)

	anthropicReq, err := prx.prepareRequestForCredential(
		req,
		body,
		body,
		"qwen-5",
		"qwen-5",
		"/v1/responses",
		false,
		&anthropicCred,
		true,
		false,
		false,
	)
	require.NoError(t, err)
	require.True(t, anthropicReq.convertedResp)
	require.False(t, anthropicReq.passthroughResponses)
	require.Equal(t, "/v1/chat/completions", anthropicReq.path)
	require.Equal(t, "/v1/responses", anthropicReq.proxyPath)
	require.Contains(t, string(anthropicReq.body), `"messages"`)
	require.NotContains(t, string(anthropicReq.body), `"input"`)
	require.Contains(t, string(anthropicReq.proxyBody), `"input"`)
	require.NotContains(t, string(anthropicReq.proxyBody), `"messages"`)
}

func TestProxyRequest_ResponsesRetryRecomputesProviderMode(t *testing.T) {
	var openaiCalls int32
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&openaiCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer openaiSrv.Close()

	var anthropicCalls int32
	var anthropicPath string
	var anthropicBody []byte
	anthropicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&anthropicCalls, 1)
		anthropicPath = r.URL.Path
		anthropicBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"model":"qwen-5",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"ok"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer anthropicSrv.Close()

	openaiCred := config.CredentialConfig{
		Name:             "openai",
		Type:             config.ProviderTypeOpenAI,
		APIKey:           "key",
		BaseURL:          openaiSrv.URL,
		RPM:              100,
		FallbackPriority: 10,
	}
	anthropicCred := config.CredentialConfig{
		Name:             "anthropic",
		Type:             config.ProviderTypeAnthropic,
		APIKey:           "key2",
		BaseURL:          anthropicSrv.URL,
		RPM:              100,
		FallbackPriority: 20,
	}
	prx := NewTestProxyBuilder().
		WithCredentials(openaiCred, anthropicCred).
		WithMasterKey("master-key").
		WithMaxProviderRetries(1).
		Build()

	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"qwen-5","input":"Hello","stream":false}`))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int32(1), atomic.LoadInt32(&openaiCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&anthropicCalls))
	require.Equal(t, "/v1/messages", anthropicPath)

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(anthropicBody, &raw))
	require.Contains(t, raw, "messages")
	require.NotContains(t, raw, "input")
}
