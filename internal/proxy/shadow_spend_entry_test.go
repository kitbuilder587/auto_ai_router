package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/converter"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/monitoring"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testClientKeyHash = "cc557cce629a1cb98664b98a3d5f5600a90a91c5955c4fdddfa4d13c94bfdcd6"

func TestBuildShadowSpendEntryRecordsValidContextWithoutAPIKeyHash(t *testing.T) {
	p := NewTestProxyBuilder().Build()
	p.metrics = monitoring.New(true)
	logCtx := testLogCtx(t)
	logCtx.ShadowContext = shadowcontext.Result{
		State: shadowcontext.StateValid,
		Identity: shadowcontext.Identity{
			UserID:       "tenant-user",
			PublicModel:  "public-model",
			DeploymentID: "deployment-id",
		},
	}
	before := testutil.ToFloat64(
		monitoring.ShadowSpendMissingIdentityTotal.WithLabelValues("signed_context_missing_api_key_hash"),
	)

	entry := p.buildShadowSpendEntry(logCtx)

	require.NotNil(t, entry)
	assert.Empty(t, entry.APIKey)
	assert.False(t, entry.ComparisonEligible)
	assert.Equal(t, before+1, testutil.ToFloat64(
		monitoring.ShadowSpendMissingIdentityTotal.WithLabelValues("signed_context_missing_api_key_hash"),
	))
}

func TestBuildShadowSpendEntryUsesCanonicalBillingAndSignedIdentity(t *testing.T) {
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"provider-model": {
			InputCostPerToken:           0.000001,
			OutputCostPerToken:          0.000004,
			InputCostPerCachedToken:     0.0000005,
			CacheCreationInputTokenCost: 0.000002,
		},
	})
	p := &Proxy{
		logger:             slog.New(slog.DiscardHandler),
		priceRegistry:      registry,
		spendAPIBase:       "http://air-ru01/v1",
		maxProviderRetries: 2,
	}
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	request.Header.Set("X-Forwarded-For", "192.0.2.10, 10.0.0.1")
	start := time.Now().Add(-240 * time.Millisecond)
	completionStart := start.Add(50 * time.Millisecond)
	billing := NewBillingContext("event-1", "call-1", "/v1/responses", shadowcontext.Identity{
		PublicModel:  "public-model",
		DeploymentID: "deployment-1",
	}).WithRouting("backend-model", "provider-model", "vertex-ai", "credential-1", "https://provider.example.invalid/v1")
	billing = billing.AddAttempt(BillingAttempt{Credential: "credential-1", Provider: "vertex-ai", ProviderModel: "provider-model", TargetHost: "provider.example.invalid", HTTPStatus: 200, Outcome: "success"})
	billing = billing.WithProviderResponseID("resp-1")
	logCtx := &RequestLogContext{
		RequestID: "event-1",
		CallID:    "call-1",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid, Identity: shadowcontext.Identity{
			APIKeyHash:     testClientKeyHash,
			UserID:         "user-1",
			TeamID:         "team-1",
			OrganizationID: "org-1",
			ProjectID:      "project-1",
			AgentID:        "agent-1",
			PublicModel:    "public-model",
			DeploymentID:   "deployment-1",
			EndUser:        "end-user-1",
			Tags:           []string{"tag-1"},
			CallID:         "call-1",
		}},
		Billing: billing,
		TokenInfo: &litellmdb.TokenInfo{
			Token:          "attacker-token",
			UserID:         "attacker-user",
			TeamID:         "attacker-team",
			OrganizationID: "attacker-org",
			ProjectID:      "attacker-project",
			AgentID:        "attacker-agent",
			Tags:           []string{"attacker-tag"},
		},
		StartTime:           start,
		CompletionStartTime: completionStart,
		Request:             request,
		Status:              "success",
		HTTPStatus:          200,
		TokenUsage: &converter.TokenUsage{
			PromptTokens:             120,
			CompletionTokens:         30,
			AudioInputTokens:         5,
			AudioOutputTokens:        2,
			CachedInputTokens:        20,
			CacheCreationTokens:      10,
			ReasoningTokens:          5,
			AcceptedPredictionTokens: 2,
			RejectedPredictionTokens: 1,
		},
		Credential:  &config.CredentialConfig{Name: "credential-1", Type: config.ProviderTypeVertexAI},
		SessionID:   "session-1",
		TargetURL:   "https://provider.example.invalid/v1",
		RealModelID: "provider-model",
		ModelID:     "backend-model",
	}

	entry := p.buildShadowSpendEntry(logCtx)

	require.NotNil(t, entry)
	assert.Equal(t, "resp-1", entry.RequestID)
	assert.Equal(t, "event-1", entry.AirEventID)
	assert.Equal(t, "aresponses", entry.CallType)
	assert.Equal(t, "backend-model", entry.Model)
	assert.Equal(t, "public-model", entry.ModelGroup)
	assert.Equal(t, "deployment-1", entry.ModelID)
	assert.Equal(t, "openai", entry.CustomLLMProvider)
	assert.Equal(t, "http://air-ru01/v1", entry.APIBase)
	assert.Equal(t, testClientKeyHash, entry.APIKey)
	assert.Equal(t, "user-1", entry.UserID)
	assert.Equal(t, "team-1", entry.TeamID)
	assert.Equal(t, "org-1", entry.OrganizationID)
	assert.Equal(t, "project-1", entry.ProjectID)
	assert.Equal(t, "end-user-1", entry.EndUser)
	assert.Equal(t, "agent-1", entry.AgentID)
	assert.JSONEq(t, `["tag-1"]`, entry.RequestTags)
	assert.True(t, entry.ComparisonEligible)
	assert.Equal(t, 150, entry.TotalTokens)
	assert.Greater(t, entry.RequestDurationMS, 0)
	require.NotNil(t, entry.CompletionStartTime)
	assert.Equal(t, completionStart, *entry.CompletionStartTime)

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	assert.Equal(t, "call-1", metadata["litellm_call_id"])
	assert.Equal(t, "success", metadata["status"])
	assert.EqualValues(t, 0, metadata["attempted_retries"])
	assert.EqualValues(t, 2, metadata["max_retries"])
	assert.Equal(t, "project-1", metadata["user_api_key_project_id"])
	usageObject := metadata["usage_object"].(map[string]any)
	promptDetails := usageObject["prompt_tokens_details"].(map[string]any)
	completionDetails := usageObject["completion_tokens_details"].(map[string]any)
	assert.EqualValues(t, 5, promptDetails["audio_tokens"])
	assert.EqualValues(t, 20, promptDetails["cached_tokens"])
	assert.EqualValues(t, 10, promptDetails["cache_creation_tokens"])
	assert.EqualValues(t, 2, completionDetails["audio_tokens"])
	assert.EqualValues(t, 5, completionDetails["reasoning_tokens"])
	assert.EqualValues(t, 2, completionDetails["accepted_prediction_tokens"])
	assert.EqualValues(t, 1, completionDetails["rejected_prediction_tokens"])
	additionalUsage := metadata["additional_usage_values"].(map[string]any)
	assert.NotContains(t, additionalUsage, "prompt_tokens")
	assert.NotContains(t, additionalUsage, "completion_tokens")
	assert.NotContains(t, additionalUsage, "total_tokens")
	assert.EqualValues(t, 20, additionalUsage["cache_read_input_tokens"])
	assert.EqualValues(t, 10, additionalUsage["cache_creation_input_tokens"])
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "resp-1", extension["provider_response_id"])
	assert.Equal(t, true, extension["comparison_eligible"])
	assert.Equal(t, "valid", extension["shadow_context_state"])
	assert.Equal(t, "vertex-ai", extension["actual_provider"])
	assert.Equal(t, "credential-1", extension["actual_credential"])
	assert.Equal(t, "provider.example.invalid", extension["actual_upstream_host"])
	assert.Equal(t, "found", extension["price_status"])
	assert.Equal(t, "calculated", extension["cost_status"])
	assert.NotNil(t, extension["price_snapshot"])
	cost := metadata["cost_breakdown"].(map[string]any)
	assert.InDelta(t, 0.00024, entry.Spend, 1e-12)
	assert.InDelta(t, entry.Spend, cost["total_cost"].(float64), 1e-12)
	assert.InDelta(t,
		cost["input_cost"].(float64)+cost["cache_read_cost"].(float64)+
			cost["cache_creation_cost"].(float64)+cost["output_cost"].(float64),
		entry.Spend, 1e-12,
	)
}

func TestBuildShadowSpendEntryUsesAuthenticatedTokenInfoForDirectAIR(t *testing.T) {
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"provider-model": {InputCostPerToken: 0.000001, OutputCostPerToken: 0.000004},
	})
	p := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry}
	billing := NewBillingContext("event-direct", "call-direct", "/v1/chat/completions", shadowcontext.Identity{}).
		WithPublicModel("openai/gpt-4o-mini").
		WithRouting("backend-model", "provider-model", "openai", "credential-1", "https://provider.example.invalid/v1").
		WithProviderResponseID("chatcmpl-reused")
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	declaredTools := []string{"weather", "local_time"}
	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID: "event-direct",
		CallID:    "call-direct",
		ShadowContext: shadowcontext.Result{
			State: shadowcontext.StateInvalid,
			Identity: shadowcontext.Identity{
				APIKeyHash: "unverified-key", UserID: "unverified-user", ProjectID: "unverified-project",
			},
		},
		TokenInfo: &litellmdb.TokenInfo{
			Token:          testClientKeyHash,
			KeyAlias:       "direct-key-alias",
			UserID:         "direct-user",
			TeamID:         "direct-team",
			OrganizationID: "direct-org",
			ProjectID:      "direct-project",
			AgentID:        "direct-agent",
			Tags:           []string{"direct-tag", "chat"},
		},
		Billing:           billing,
		StartTime:         time.Now().Add(-time.Millisecond),
		Request:           request,
		Status:            "success",
		HTTPStatus:        200,
		TokenUsage:        &converter.TokenUsage{PromptTokens: 10, CompletionTokens: 5},
		Credential:        &config.CredentialConfig{Name: "credential-1", Type: config.ProviderTypeOpenAI},
		SessionID:         "direct-end-user",
		ModelID:           "backend-model",
		RealModelID:       "provider-model",
		DeclaredToolNames: declaredTools,
	})
	declaredTools[0] = "mutated-after-build"

	require.NotNil(t, entry)
	assert.Equal(t, "chatcmpl-reused", entry.RequestID)
	assert.Equal(t, "event-direct", entry.AirEventID)
	assert.Equal(t, testClientKeyHash, entry.APIKey)
	assert.Equal(t, "direct-user", entry.UserID)
	assert.Equal(t, "direct-team", entry.TeamID)
	assert.Equal(t, "direct-org", entry.OrganizationID)
	assert.Equal(t, "direct-project", entry.ProjectID)
	assert.Equal(t, "direct-agent", entry.AgentID)
	assert.Equal(t, "direct-end-user", entry.EndUser)
	assert.Equal(t, "openai/gpt-4o-mini", entry.ModelGroup)
	assert.JSONEq(t, `["direct-tag","chat"]`, entry.RequestTags)
	assert.Equal(t, []string{"weather", "local_time"}, entry.DeclaredToolNames)
	assert.Equal(t, "direct-key-alias", entry.ToolKeyAlias)

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	assert.Equal(t, "direct-project", metadata["user_api_key_project_id"])
	assert.Equal(t, "direct-agent", metadata["user_api_key_agent_id"])
	assert.Equal(t, "direct-user", metadata["user_api_key_user_id"])
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "chatcmpl-reused", extension["provider_response_id"])
	assert.Equal(t, "invalid", extension["shadow_context_state"])
}

func TestBuildShadowSpendEntryDirectAIRPersistsSafeRequestFieldsAndSelectedDeployment(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	modelManager := models.New(logger, 100, nil)
	credentials := []config.CredentialConfig{
		{Name: "primary", Type: config.ProviderTypeOpenAI},
		{Name: "fallback", Type: config.ProviderTypeOpenAI},
	}
	modelManager.UpdateDBModels([]config.ModelRPMConfig{
		{Name: "openai/gpt-4o-mini", Model: "provider-model", Credential: "primary", DeploymentID: "deployment-primary", RPM: -1, TPM: -1},
		{Name: "openai/gpt-4o-mini", Model: "provider-model", Credential: "fallback", DeploymentID: "deployment-fallback", RPM: -1, TPM: -1},
	}, nil, credentials)
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"provider-model": {InputCostPerToken: 0.000001, OutputCostPerToken: 0.000004},
	})
	p := &Proxy{
		logger:        logger,
		modelManager:  modelManager,
		priceRegistry: registry,
	}

	requestMetadata, requestTags := extractSpendRequestFields([]byte(`{
		"metadata":{
			"canary_ratio":100,
			"canary_index":3,
			"shape":{"nested":[true,0,"",null,false]},
			"custom_nested":{"user_api_key_end_user_id":"benign-when-nested"},
			"user_api_key_agent_id":"spoofed-agent",
			"user_api_key_end_user_id":"spoofed-end-user",
			"user_api_key_future_budget_field":999,
			"litellm_future_router_field":"spoofed-router-value",
			"model_id":"spoofed-model-id",
			"tags":["spoofed-metadata-tag"],
			"cost_breakdown":{"total_cost":999},
			"litellm_call_id":"spoofed-call",
			"spend_logs_metadata":{"actual_credential":"spoofed-credential"},
			"status":"failure"
		},
		"tags":["key-tag","request-tag","request-tag",""]
	}`), "application/json")
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	billing := NewBillingContext("event-direct", "trusted-call", "/v1/chat/completions", shadowcontext.Identity{}).
		WithPublicModel("openai/gpt-4o-mini").
		WithRouting("gpt-4o-mini", "provider-model", "openai", "primary", "https://primary.example.invalid/v1")
	primary := credentials[0]
	logCtx := &RequestLogContext{
		RequestID:       "event-direct",
		CallID:          "trusted-call",
		ShadowContext:   shadowcontext.Result{State: shadowcontext.StateMissing},
		Billing:         billing,
		StartTime:       time.Now().Add(-time.Millisecond),
		Request:         request,
		TokenInfo:       &litellmdb.TokenInfo{Token: testClientKeyHash, AgentID: "trusted-agent", Tags: []string{"identity-tag", "key-tag"}},
		Status:          "success",
		HTTPStatus:      http.StatusOK,
		TokenUsage:      &converter.TokenUsage{PromptTokens: 2, CompletionTokens: 3},
		Credential:      &primary,
		RequestMetadata: requestMetadata,
		RequestTags:     requestTags,
	}

	entry := p.buildShadowSpendEntry(logCtx)
	require.NotNil(t, entry)
	assert.Equal(t, "deployment-primary", entry.ModelID)
	assert.JSONEq(t, `["identity-tag","key-tag","request-tag"]`, entry.RequestTags)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	assert.EqualValues(t, 100, metadata["canary_ratio"])
	assert.EqualValues(t, 3, metadata["canary_index"])
	assert.Equal(t, map[string]any{"nested": []any{true, float64(0), "", nil, false}}, metadata["shape"])
	assert.Equal(t, map[string]any{"user_api_key_end_user_id": "benign-when-nested"}, metadata["custom_nested"])
	assert.Equal(t, "trusted-agent", metadata["user_api_key_agent_id"], "request metadata must not override authenticated identity")
	assert.Equal(t, "trusted-call", metadata["litellm_call_id"], "request metadata must not override correlation")
	assert.NotContains(t, metadata, "user_api_key_end_user_id")
	assert.NotContains(t, metadata, "user_api_key_future_budget_field")
	assert.NotContains(t, metadata, "litellm_future_router_field")
	assert.NotContains(t, metadata, "model_id")
	assert.NotContains(t, metadata, "tags")
	assert.Equal(t, "success", metadata["status"])
	costBreakdown := metadata["cost_breakdown"].(map[string]any)
	assert.Equal(t, entry.Spend, costBreakdown["total_cost"], "request metadata must not override measured cost")
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "primary", extension["actual_credential"])
	assert.Equal(t, true, extension["comparison_eligible"])

	// Reusing the immutable billing value for a fallback must resolve the final
	// credential's deployment instead of retaining the primary route's ID.
	fallback := credentials[1]
	logCtx.Credential = &fallback
	logCtx.Billing = logCtx.Billing.WithRouting(
		"gpt-4o-mini", "provider-model", "openai", "fallback", "https://fallback.example.invalid/v1",
	)
	fallbackEntry := p.buildShadowSpendEntry(logCtx)
	require.NotNil(t, fallbackEntry)
	assert.Equal(t, "deployment-fallback", fallbackEntry.ModelID)
	var fallbackMetadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(fallbackEntry.Metadata), &fallbackMetadata))
	fallbackExtension := fallbackMetadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "fallback", fallbackExtension["actual_credential"])
}

func TestBuildShadowSpendEntryUsesUniquePublicDeploymentAcrossOuterCredential(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	modelManager := models.New(logger, 100, nil)
	// FetchModelsForAIR represents an inline LiteLLM -> AIR route with a
	// synthetic outer credential. Direct AIR serves the same public model with
	// its own inner provider credential, so only the unique-public fallback is
	// meaningful for accounting attribution.
	modelManager.UpdateDBModels([]config.ModelRPMConfig{
		{
			Name:         "openai/gpt-4o-mini",
			Model:        "gpt-4o-mini",
			Credential:   "db-model-deployment-rendered",
			DeploymentID: "deployment-rendered",
			RPM:          -1,
			TPM:          -1,
		},
	}, nil, []config.CredentialConfig{{Name: "db-model-deployment-rendered"}})
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"gpt-4o-mini": {InputCostPerToken: 0.000001, OutputCostPerToken: 0.000004},
	})
	p := &Proxy{logger: logger, modelManager: modelManager, priceRegistry: registry}
	innerCredential := config.CredentialConfig{Name: "mock-openai", Type: config.ProviderTypeOpenAI}
	billing := NewBillingContext("event-rendered", "call-rendered", "/v1/chat/completions", shadowcontext.Identity{}).
		WithPublicModel("openai/gpt-4o-mini").
		WithRouting("gpt-4o-mini", "gpt-4o-mini", "openai", innerCredential.Name, "https://provider.example.invalid/v1")

	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-rendered",
		CallID:        "call-rendered",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateMissing},
		Billing:       billing,
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       httptest.NewRequest("POST", "/v1/chat/completions", nil),
		TokenInfo:     &litellmdb.TokenInfo{Token: testClientKeyHash},
		Status:        "success",
		HTTPStatus:    http.StatusOK,
		TokenUsage:    &converter.TokenUsage{PromptTokens: 1, CompletionTokens: 1},
		Credential:    &innerCredential,
	})

	require.NotNil(t, entry)
	assert.Equal(t, "deployment-rendered", entry.ModelID)
	assert.Equal(t, "openai/gpt-4o-mini", entry.ModelGroup)
}

func TestBuildShadowSpendEntrySignedIdentityKeepsDeploymentAndTagsAuthoritative(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	modelManager := models.New(logger, 100, nil)
	credentials := []config.CredentialConfig{{Name: "direct-route", Type: config.ProviderTypeOpenAI}}
	modelManager.UpdateDBModels([]config.ModelRPMConfig{
		{Name: "signed/public-model", Credential: "direct-route", DeploymentID: "direct-deployment", RPM: -1, TPM: -1},
	}, nil, credentials)
	p := &Proxy{logger: logger, modelManager: modelManager}
	signedIdentity := shadowcontext.Identity{
		APIKeyHash:   testClientKeyHash,
		AgentID:      "signed-agent",
		PublicModel:  "signed/public-model",
		DeploymentID: "signed-deployment",
		Tags:         []string{"signed-tag"},
	}
	billing := NewBillingContext("event-signed", "signed-call", "/v1/chat/completions", signedIdentity).
		WithRouting("backend", "provider", "openai", "direct-route", "https://provider.example.invalid/v1")
	credential := credentials[0]
	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-signed",
		CallID:        "signed-call",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid, Identity: signedIdentity},
		Billing:       billing,
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       httptest.NewRequest("POST", "/v1/chat/completions", nil),
		TokenInfo:     &litellmdb.TokenInfo{Token: "direct-token", AgentID: "direct-agent", Tags: []string{"direct-token-tag"}},
		Status:        "success",
		HTTPStatus:    http.StatusOK,
		TokenUsage:    &converter.TokenUsage{},
		Credential:    &credential,
		RequestMetadata: map[string]any{
			"user_api_key_agent_id":         "spoofed-agent",
			"user_api_key_end_user_id":      "spoofed-end-user",
			"user_api_key_unknown_identity": "spoofed-future-field",
			"litellm_unknown_routing":       "spoofed-routing-field",
			"response_cost":                 999,
			"custom":                        "preserved",
			"custom_nested":                 map[string]any{"litellm_unknown_routing": "benign-when-nested"},
		},
		RequestTags: []string{"untrusted-request-tag"},
	})

	require.NotNil(t, entry)
	assert.Equal(t, "signed-deployment", entry.ModelID)
	assert.JSONEq(t, `["signed-tag"]`, entry.RequestTags)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	assert.Equal(t, "signed-agent", metadata["user_api_key_agent_id"])
	assert.Equal(t, "preserved", metadata["custom"])
	assert.Equal(t, map[string]any{"litellm_unknown_routing": "benign-when-nested"}, metadata["custom_nested"])
	assert.NotContains(t, metadata, "user_api_key_end_user_id")
	assert.NotContains(t, metadata, "user_api_key_unknown_identity")
	assert.NotContains(t, metadata, "litellm_unknown_routing")
	assert.NotContains(t, metadata, "response_cost")
}

func TestBuildShadowSpendEntryFallbackUsesCapturedPublicModel(t *testing.T) {
	p := &Proxy{logger: slog.New(slog.DiscardHandler)}
	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-fallback",
		CallID:        "call-fallback",
		PublicModelID: "openai/gpt-4o-mini",
		ModelID:       "backend-gpt-4o-mini",
		RealModelID:   "provider-gpt-4o-mini",
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       httptest.NewRequest("POST", "/v1/chat/completions", nil),
		TokenInfo:     &litellmdb.TokenInfo{Token: testClientKeyHash},
		Status:        "success",
		HTTPStatus:    http.StatusOK,
		TokenUsage:    &converter.TokenUsage{},
		Credential:    &config.CredentialConfig{Name: "credential-1", Type: config.ProviderTypeOpenAI},
		TargetURL:     "https://provider.example.invalid/v1",
	})

	require.NotNil(t, entry)
	assert.Equal(t, "openai/gpt-4o-mini", entry.ModelGroup)
	assert.Equal(t, "backend-gpt-4o-mini", entry.Model)
}

func TestBuildShadowSpendEntryWritesEmptyTagArrayForDirectAIR(t *testing.T) {
	p := &Proxy{logger: slog.New(slog.DiscardHandler)}
	billing := NewBillingContext("event-direct", "call-direct", "/v1/chat/completions", shadowcontext.Identity{}).
		WithRouting("backend-model", "provider-model", "openai", "credential-1", "https://provider.example.invalid/v1")
	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-direct",
		CallID:        "call-direct",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateMissing},
		TokenInfo:     &litellmdb.TokenInfo{Token: testClientKeyHash},
		Billing:       billing,
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       httptest.NewRequest("POST", "/v1/chat/completions", nil),
		Status:        "success",
		HTTPStatus:    200,
		TokenUsage:    &converter.TokenUsage{},
		Credential:    &config.CredentialConfig{Name: "credential-1", Type: config.ProviderTypeOpenAI},
	})

	require.NotNil(t, entry)
	assert.Equal(t, "[]", entry.RequestTags)
	assert.NotEqual(t, "null", entry.RequestTags)
}

func TestBuildShadowSpendEntryPricesImagesWithoutProviderUsage(t *testing.T) {
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"gpt-image-1": {OutputCostPerImage: 0.02},
	})
	p := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry, spendAPIBase: "http://air-ru01/v1"}
	identity := shadowcontext.Identity{
		APIKeyHash: testClientKeyHash, PublicModel: "public-image", DeploymentID: "image-deployment", CallID: "call-image",
	}
	billing := NewBillingContext("event-image", "call-image", "/v1/images/generations", identity).
		WithRouting("gpt-image-1", "gpt-image-1", "openai", "image-credential", "https://images.example.invalid/v1")
	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:         "event-image",
		CallID:            "call-image",
		ShadowContext:     shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
		Billing:           billing,
		StartTime:         time.Now().Add(-time.Millisecond),
		Request:           httptest.NewRequest("POST", "/v1/images/generations", nil),
		Status:            "success",
		HTTPStatus:        200,
		Credential:        &config.CredentialConfig{Name: "image-credential", Type: config.ProviderTypeOpenAI},
		ModelID:           "gpt-image-1",
		RealModelID:       "gpt-image-1",
		IsImageGeneration: true,
		ImageCount:        2,
	})

	require.NotNil(t, entry)
	assert.InDelta(t, 0.04, entry.Spend, 1e-12)
	assert.True(t, entry.ComparisonEligible)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "request_parameters", extension["usage_source"])
	assert.Equal(t, "found", extension["price_status"])
	assert.Equal(t, "calculated", extension["cost_status"])
}

func TestBuildShadowSpendEntryPricesImageEndpointOutputTokensAtImageRate(t *testing.T) {
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"gpt-image-token": {
			InputCostPerToken:       0.000001,
			OutputCostPerToken:      0.000002,
			InputCostPerImageToken:  0.000003,
			OutputCostPerImageToken: 0.000005,
		},
	})
	p := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry, spendAPIBase: "http://air-ru01/v1"}
	identity := shadowcontext.Identity{
		APIKeyHash: testClientKeyHash, PublicModel: "public-image", DeploymentID: "image-deployment", CallID: "call-image",
	}
	billing := NewBillingContext("event-image", "call-image", "/v1/images/edits", identity).
		WithRouting("gpt-image-token", "gpt-image-token", "openai", "image-credential", "https://images.example.invalid/v1")
	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:         "event-image",
		CallID:            "call-image",
		ShadowContext:     shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
		Billing:           billing,
		StartTime:         time.Now().Add(-time.Millisecond),
		Request:           httptest.NewRequest("POST", "/v1/images/edits", nil),
		Status:            "success",
		HTTPStatus:        200,
		Credential:        &config.CredentialConfig{Name: "image-credential", Type: config.ProviderTypeOpenAI},
		ModelID:           "gpt-image-token",
		RealModelID:       "gpt-image-token",
		IsImageGeneration: true,
		ImageCount:        1,
		TokenUsage: &converter.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 500,
			ImageTokens:      40,
		},
		UsageSource: "provider",
	})

	require.NotNil(t, entry)
	assert.InDelta(t, 0.00268, entry.Spend, 1e-12)
	assert.True(t, entry.ComparisonEligible)

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	usageObject := metadata["usage_object"].(map[string]any)
	completionDetails := usageObject["completion_tokens_details"].(map[string]any)
	assert.EqualValues(t, 500, completionDetails["image_tokens"])
}

func TestBuildShadowSpendEntryPreservesOpenAIChatFailureRoute(t *testing.T) {
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"provider-model": {InputCostPerToken: 0.000001, OutputCostPerToken: 0.000004},
	})
	p := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry}
	identity := shadowcontext.Identity{
		APIKeyHash: testClientKeyHash, PublicModel: "public-model", DeploymentID: "deployment-1", CallID: "call-failure",
	}
	billing := NewBillingContext("event-failure", "call-failure", "/v1/chat/completions", identity).
		WithRouting("backend-model", "provider-model", "openai", "credential-1", "https://provider.example.invalid/v1")

	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-failure",
		CallID:        "call-failure",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
		Billing:       billing,
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       httptest.NewRequest("POST", "/v1/chat/completions", nil),
		Status:        "failure",
		HTTPStatus:    503,
		ErrorMsg:      "provider unavailable",
		Credential:    &config.CredentialConfig{Name: "credential-1", Type: config.ProviderTypeOpenAI},
		ModelID:       "backend-model",
		RealModelID:   "provider-model",
	})

	require.NotNil(t, entry)
	assert.Equal(t, "acompletion", entry.CallType)
	assert.Equal(t, "False", entry.CacheHit)
	assert.Zero(t, entry.Spend)
	assert.True(t, entry.ComparisonEligible)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "acompletion", extension["original_call_type"])
	assert.Equal(t, "missing", extension["usage_source"])
	assert.Equal(t, "calculated", extension["cost_status"])
}

func TestBuildShadowSpendEntryWithoutContextOrPriceIsIneligibleAndNeverUsesCredentialAsTeam(t *testing.T) {
	p := &Proxy{logger: slog.New(slog.DiscardHandler), spendAPIBase: "http://air-ru01/v1"}
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	billing := NewBillingContext("event-1", "generated-call", "/v1/chat/completions", shadowcontext.Identity{}).
		WithRouting("backend-model", "provider-model", "openai", "provider-credential", "https://provider.example.invalid/v1")
	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-1",
		CallID:        "generated-call",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateMissing},
		Billing:       billing,
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       request,
		Status:        "success",
		HTTPStatus:    200,
		TokenUsage:    &converter.TokenUsage{PromptTokens: 1, CompletionTokens: 1},
		Credential:    &config.CredentialConfig{Name: "provider-credential", Type: config.ProviderTypeOpenAI},
		ModelID:       "backend-model",
		RealModelID:   "provider-model",
	})

	require.NotNil(t, entry)
	assert.Equal(t, "event-1", entry.RequestID)
	assert.Equal(t, "event-1", entry.AirEventID)
	assert.Empty(t, entry.APIKey)
	assert.Empty(t, entry.TeamID)
	assert.Empty(t, entry.ModelGroup)
	assert.Empty(t, entry.ModelID)
	assert.False(t, entry.ComparisonEligible)

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, false, extension["comparison_eligible"])
	assert.Equal(t, "missing", extension["shadow_context_state"])
	assert.Equal(t, "missing_registry", extension["price_status"])
	assert.Equal(t, "price_missing", extension["cost_status"])
}

func TestBuildShadowSpendEntryWithoutUsageIsFinanciallyIneligible(t *testing.T) {
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"provider-model": {InputCostPerToken: 0.000001, OutputCostPerToken: 0.000004},
	})
	p := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry}
	identity := shadowcontext.Identity{
		APIKeyHash: testClientKeyHash, PublicModel: "public-model", DeploymentID: "deployment-1", CallID: "call-1",
	}
	billing := NewBillingContext("event-1", "call-1", "/v1/chat/completions", identity).
		WithRouting("backend-model", "provider-model", "openai", "credential-1", "https://provider.example.invalid/v1")

	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-1",
		CallID:        "call-1",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
		Billing:       billing,
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       httptest.NewRequest("POST", "/v1/chat/completions", nil),
		Status:        "success",
		HTTPStatus:    200,
		Credential:    &config.CredentialConfig{Name: "credential-1", Type: config.ProviderTypeOpenAI},
		ModelID:       "backend-model",
		RealModelID:   "provider-model",
	})

	require.NotNil(t, entry)
	assert.False(t, entry.ComparisonEligible)
	assert.Zero(t, entry.Spend)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "missing", extension["usage_source"])
	assert.Equal(t, "found", extension["price_status"])
	assert.Equal(t, "insufficient_usage", extension["cost_status"])
	assert.Equal(t, false, extension["comparison_eligible"])
	assert.Nil(t, metadata["cost_breakdown"])
}

func TestBuildShadowSpendEntryWithMissingPriceIsFinanciallyIneligible(t *testing.T) {
	p := &Proxy{logger: slog.New(slog.DiscardHandler)}
	identity := shadowcontext.Identity{
		APIKeyHash: testClientKeyHash, PublicModel: "public-model", DeploymentID: "deployment-1", CallID: "call-1",
	}
	billing := NewBillingContext("event-1", "call-1", "/v1/chat/completions", identity).
		WithRouting("backend-model", "unpriced-provider-model", "openai", "credential-1", "https://provider.example.invalid/v1")

	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-1",
		CallID:        "call-1",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
		Billing:       billing,
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       httptest.NewRequest("POST", "/v1/chat/completions", nil),
		Status:        "success",
		HTTPStatus:    200,
		TokenUsage:    &converter.TokenUsage{PromptTokens: 10, CompletionTokens: 5},
		Credential:    &config.CredentialConfig{Name: "credential-1", Type: config.ProviderTypeOpenAI},
		ModelID:       "backend-model",
		RealModelID:   "unpriced-provider-model",
	})

	require.NotNil(t, entry)
	assert.False(t, entry.ComparisonEligible)
	assert.Zero(t, entry.Spend)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "missing_registry", extension["price_status"])
	assert.Equal(t, "price_missing", extension["cost_status"])
	assert.Nil(t, metadata["cost_breakdown"])
}

func TestBuildShadowSpendEntryPriceSnapshotUsesResolvedBackendFallback(t *testing.T) {
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{"backend-model": {InputCostPerToken: 0.000001}})
	p := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry}
	identity := shadowcontext.Identity{
		APIKeyHash: testClientKeyHash, PublicModel: "public-model", DeploymentID: "deployment-1", CallID: "call-1",
	}
	billing := NewBillingContext("event-1", "call-1", "/v1/chat/completions", identity).
		WithRouting("backend-model", "unpriced-provider-alias", "openai", "credential-1", "https://provider.example.invalid/v1")

	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:     "event-1",
		CallID:        "call-1",
		ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
		Billing:       billing,
		StartTime:     time.Now().Add(-time.Millisecond),
		Request:       httptest.NewRequest("POST", "/v1/chat/completions", nil),
		Status:        "success",
		HTTPStatus:    200,
		TokenUsage:    &converter.TokenUsage{PromptTokens: 10},
		Credential:    &config.CredentialConfig{Name: "credential-1", Type: config.ProviderTypeOpenAI},
		ModelID:       "backend-model",
		RealModelID:   "unpriced-provider-alias",
	})

	require.NotNil(t, entry)
	assert.True(t, entry.ComparisonEligible)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	extension := metadata["spend_logs_metadata"].(map[string]any)
	snapshot := extension["price_snapshot"].(map[string]any)
	assert.Equal(t, "found", extension["price_status"])
	assert.Equal(t, "calculated", extension["cost_status"])
	assert.Equal(t, "backend-model", snapshot["model"])
}

func TestBuildShadowSpendEntryTokenPricedImageWithoutUsageIsIneligible(t *testing.T) {
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"image-model": {InputCostPerToken: 0.000001, OutputCostPerImageToken: 0.000004},
	})
	p := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry}
	identity := shadowcontext.Identity{
		APIKeyHash: testClientKeyHash, PublicModel: "public-image", DeploymentID: "deployment-image", CallID: "call-image",
	}
	billing := NewBillingContext("event-image", "call-image", "/v1/images/generations", identity).
		WithRouting("image-model", "image-model", "openai", "credential-image", "https://provider.example.invalid/v1")

	entry := p.buildShadowSpendEntry(&RequestLogContext{
		RequestID:         "event-image",
		CallID:            "call-image",
		ShadowContext:     shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
		Billing:           billing,
		StartTime:         time.Now().Add(-time.Millisecond),
		Request:           httptest.NewRequest("POST", "/v1/images/generations", nil),
		Status:            "success",
		HTTPStatus:        200,
		Credential:        &config.CredentialConfig{Name: "credential-image", Type: config.ProviderTypeOpenAI},
		ModelID:           "image-model",
		RealModelID:       "image-model",
		IsImageGeneration: true,
		ImageCount:        2,
	})

	require.NotNil(t, entry)
	assert.False(t, entry.ComparisonEligible)
	assert.Zero(t, entry.Spend)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	extension := metadata["spend_logs_metadata"].(map[string]any)
	assert.Equal(t, "request_parameters", extension["usage_source"])
	assert.Equal(t, "found", extension["price_status"])
	assert.Equal(t, "insufficient_usage", extension["cost_status"])
	assert.Nil(t, metadata["cost_breakdown"])
}

func TestBuildShadowSpendEntryMatchesGoldenCategoricalFields(t *testing.T) {
	type rawRow struct {
		RequestID          string   `json:"request_id"`
		CallType           string   `json:"call_type"`
		APIKey             string   `json:"api_key"`
		Model              string   `json:"model"`
		ModelID            string   `json:"model_id"`
		ModelGroup         string   `json:"model_group"`
		CustomProvider     string   `json:"custom_llm_provider"`
		APIBase            string   `json:"api_base"`
		UserID             string   `json:"user"`
		TeamID             string   `json:"team_id"`
		OrganizationID     string   `json:"organization_id"`
		EndUser            string   `json:"end_user"`
		AgentID            string   `json:"agent_id"`
		RequestTags        []string `json:"request_tags"`
		Status             string   `json:"status"`
		RequesterIPAddress string   `json:"requester_ip_address"`
		Metadata           struct {
			CallID    string `json:"litellm_call_id"`
			ProjectID string `json:"user_api_key_project_id"`
		} `json:"metadata"`
	}
	type fixture struct {
		Scenario struct {
			Endpoint string `json:"endpoint"`
		} `json:"scenario"`
		Raw rawRow `json:"raw_row"`
	}

	files, err := filepath.Glob("../../testdata/golden/shadow-spend/*.json")
	require.NoError(t, err)
	require.Len(t, files, 6)
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			contents, err := os.ReadFile(file)
			require.NoError(t, err)
			var golden fixture
			require.NoError(t, json.Unmarshal(contents, &golden))

			identity := shadowcontext.Identity{
				APIKeyHash:     golden.Raw.APIKey,
				UserID:         golden.Raw.UserID,
				TeamID:         golden.Raw.TeamID,
				OrganizationID: golden.Raw.OrganizationID,
				ProjectID:      golden.Raw.Metadata.ProjectID,
				AgentID:        golden.Raw.AgentID,
				PublicModel:    golden.Raw.ModelGroup,
				DeploymentID:   golden.Raw.ModelID,
				EndUser:        golden.Raw.EndUser,
				Tags:           golden.Raw.RequestTags,
				CallID:         golden.Raw.Metadata.CallID,
			}
			billing := NewBillingContext(golden.Raw.RequestID, golden.Raw.Metadata.CallID, golden.Scenario.Endpoint, identity).
				WithRouting(golden.Raw.Model, golden.Raw.Model, "provider-fixture", "credential-fixture", "https://provider.fixture.invalid/v1")
			if !strings.HasPrefix(golden.Raw.RequestID, "air-event-") {
				billing = billing.WithProviderResponseID(golden.Raw.RequestID)
			}
			registry := models.NewModelPriceRegistry()
			registry.Update(map[string]*models.ModelPrice{golden.Raw.Model: {InputCostPerToken: 0.000001}})
			p := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry, spendAPIBase: golden.Raw.APIBase}
			request := httptest.NewRequest("POST", golden.Scenario.Endpoint, nil)
			request.Header.Set("X-Forwarded-For", golden.Raw.RequesterIPAddress)

			entry := p.buildShadowSpendEntry(&RequestLogContext{
				RequestID:     golden.Raw.RequestID,
				CallID:        golden.Raw.Metadata.CallID,
				ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
				Billing:       billing,
				StartTime:     time.Now().Add(-time.Millisecond),
				Request:       request,
				Status:        golden.Raw.Status,
				HTTPStatus:    200,
				TokenUsage:    &converter.TokenUsage{PromptTokens: 1},
				Credential:    &config.CredentialConfig{Name: "credential-fixture", Type: config.ProviderTypeOpenAI},
				ModelID:       golden.Raw.Model,
				RealModelID:   golden.Raw.Model,
			})

			require.NotNil(t, entry)
			assert.Equal(t, golden.Raw.RequestID, entry.RequestID)
			assert.Equal(t, golden.Raw.CallType, entry.CallType)
			assert.Equal(t, golden.Raw.APIKey, entry.APIKey)
			assert.Equal(t, golden.Raw.Model, entry.Model)
			assert.Equal(t, golden.Raw.ModelID, entry.ModelID)
			assert.Equal(t, golden.Raw.ModelGroup, entry.ModelGroup)
			assert.Equal(t, golden.Raw.CustomProvider, entry.CustomLLMProvider)
			assert.Equal(t, golden.Raw.APIBase, entry.APIBase)
			assert.Equal(t, golden.Raw.UserID, entry.UserID)
			assert.Equal(t, golden.Raw.TeamID, entry.TeamID)
			assert.Equal(t, golden.Raw.OrganizationID, entry.OrganizationID)
			assert.Equal(t, golden.Raw.EndUser, entry.EndUser)
			assert.Equal(t, golden.Raw.AgentID, entry.AgentID)
			assert.Equal(t, golden.Raw.Status, entry.Status)
			assert.Equal(t, golden.Raw.RequesterIPAddress, entry.RequesterIP)
			assert.True(t, entry.ComparisonEligible)
			var tags []string
			require.NoError(t, json.Unmarshal([]byte(entry.RequestTags), &tags))
			assert.Equal(t, golden.Raw.RequestTags, tags)
		})
	}
}
