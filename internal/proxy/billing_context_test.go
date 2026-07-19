package proxy

import (
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteKindFromOriginalEndpoint(t *testing.T) {
	tests := map[string]RouteKind{
		"/v1/chat/completions":   RouteCompletion,
		"/v1/completions":        RouteTextCompletion,
		"/v1/embeddings":         RouteEmbedding,
		"/v1/responses":          RouteResponses,
		"/v1/images/generations": RouteImageGeneration,
		"/v1/images/edits":       RouteImageEdit,
	}
	for endpoint, expected := range tests {
		assert.Equal(t, expected, routeKindFromEndpoint(endpoint), endpoint)
	}
}

func TestRequestLogContextKeepsFirstCompletionStart(t *testing.T) {
	first := time.Unix(1_800_000_000, 0).UTC()
	logCtx := &RequestLogContext{}
	logCtx.markCompletionStart(first)
	logCtx.markCompletionStart(first.Add(time.Second))

	assert.Equal(t, first, logCtx.CompletionStartTime)
}

func TestBillingContextPreservesOriginalRouteAndPublicModel(t *testing.T) {
	ctx := NewBillingContext("event-1", "call-1", "/v1/responses", shadowcontext.Identity{
		PublicModel:  "public-model",
		DeploymentID: "deployment-1",
	})
	resolved := ctx.WithRouting("backend-model", "provider-model", "openai", "credential-1", "https://provider.example/v1/chat/completions")
	fallback := resolved.WithRouting("mutated-backend", "fallback-provider-model", "proxy", "fallback", "https://fallback.example/v1").
		AddAttempt(BillingAttempt{Credential: "fallback", ProviderModel: "fallback-provider-model"})
	completed := fallback.CompleteLastAttempt(429, "provider_error")

	assert.Equal(t, RouteResponses, fallback.CallType())
	assert.Equal(t, "/v1/responses", fallback.OriginalEndpoint())
	assert.Equal(t, "public-model", fallback.PublicModel())
	assert.Equal(t, "deployment-1", fallback.DeploymentID())
	assert.Equal(t, "backend-model", fallback.BackendModel())
	assert.Equal(t, "fallback-provider-model", fallback.ProviderModel())
	assert.Empty(t, ctx.BackendModel(), "value-style update must not mutate the original context")
	require.Len(t, fallback.Attempts(), 1)
	assert.Empty(t, fallback.Attempts()[0].Outcome, "completing an attempt must not mutate an earlier snapshot")
	assert.Equal(t, "provider_error", completed.Attempts()[0].Outcome)
	assert.Equal(t, 429, completed.Attempts()[0].HTTPStatus)
	assert.Empty(t, resolved.Attempts(), "adding an attempt must not mutate an earlier context value")
}

func TestBillingContextUsesOnlyCanonicalSignedOriginalCallType(t *testing.T) {
	translated := NewBillingContext("event-1", "call-1", "/v1/chat/completions", shadowcontext.Identity{
		OriginalCallType: string(RouteTextCompletion),
	})
	assert.Equal(t, RouteTextCompletion, translated.CallType())
	assert.Equal(t, "/v1/chat/completions", translated.OriginalEndpoint())

	legacyToken := NewBillingContext("event-2", "call-2", "/v1/chat/completions", shadowcontext.Identity{})
	assert.Equal(t, RouteCompletion, legacyToken.CallType())

	invalidIdentity := NewBillingContext("event-3", "call-3", "/v1/chat/completions", shadowcontext.Identity{
		OriginalCallType: "text_completion",
	})
	assert.Equal(t, RouteCompletion, invalidIdentity.CallType())
}

func TestBillingContextFillsMissingPublicModelWithoutOverwritingSignedIdentity(t *testing.T) {
	direct := NewBillingContext("event-direct", "call-direct", "/v1/chat/completions", shadowcontext.Identity{}).
		WithPublicModel("openai/gpt-4o-mini")
	assert.Equal(t, "openai/gpt-4o-mini", direct.PublicModel())

	chained := NewBillingContext("event-chained", "call-chained", "/v1/chat/completions", shadowcontext.Identity{
		PublicModel: "signed/public-model",
	}).WithPublicModel("caller-controlled-model")
	assert.Equal(t, "signed/public-model", chained.PublicModel(), "verified chained identity must remain authoritative")
}

func TestBillingContextSpendRequestIDPrefersProviderResponseID(t *testing.T) {
	ctx := NewBillingContext("event-1", "call-1", "/v1/chat/completions", shadowcontext.Identity{})
	assert.Equal(t, "event-1", ctx.SpendRequestID())

	ctx = ctx.WithProviderResponseID("chatcmpl-123")
	assert.Equal(t, "chatcmpl-123", ctx.SpendRequestID())
	assert.Equal(t, "chatcmpl-123", ctx.ProviderResponseID())

	ctx = ctx.WithProviderResponseID("unexpected-second-id")
	assert.Equal(t, "chatcmpl-123", ctx.SpendRequestID())
	assert.Equal(t, "chatcmpl-123", ctx.ProviderResponseID())

	legacy := BillingContext{callID: "call-only", providerResponseID: "provider-controlled"}
	assert.Equal(t, "provider-controlled", legacy.SpendRequestID())
}

func TestBillingContextExposesReusedProviderIDAndDistinctEventFallbacks(t *testing.T) {
	first := NewBillingContext("event-1", "shared-call", "/v1/chat/completions", shadowcontext.Identity{}).
		WithProviderResponseID("chatcmpl-reused")
	second := NewBillingContext("event-2", "shared-call", "/v1/chat/completions", shadowcontext.Identity{}).
		WithProviderResponseID("chatcmpl-reused")

	assert.Equal(t, first.SpendRequestID(), second.SpendRequestID())
	assert.Equal(t, "chatcmpl-reused", first.SpendRequestID())
	assert.Equal(t, "event-1", first.EventID())
	assert.Equal(t, "event-2", second.EventID())
}

func TestExtractClientVisibleResponseID(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "chat", body: `{"id":"chatcmpl-1","object":"chat.completion"}`, want: "chatcmpl-1"},
		{name: "responses", body: `{"id":"resp_1","object":"response"}`, want: "resp_1"},
		{name: "nested completed event", body: `{"type":"response.completed","response":{"id":"resp_2"}}`, want: "resp_2"},
		{name: "sse", body: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_3\"}}\n\n", want: "resp_3"},
		{name: "no id", body: `{"created":123,"data":[]}`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractClientVisibleResponseID([]byte(tt.body)))
		})
	}
}

func TestResponseIDCaptureHandlesSplitSSEReads(t *testing.T) {
	var capture responseIDCapture
	assert.Empty(t, capture.Observe([]byte(`data: {"id":"chatcmpl-`)))
	assert.Equal(t, "chatcmpl-split", capture.Observe([]byte("split\",\"choices\":[]}\n\n")))
	assert.Equal(t, "chatcmpl-split", capture.Observe([]byte("data: {\"id\":\"different\"}\n\n")))
}

func TestTargetHostNeverPersistsURLPathOrSecrets(t *testing.T) {
	tests := map[string]string{
		"https://provider.example.invalid/v1?token=secret":       "provider.example.invalid",
		"https://user:password@provider.example.invalid:8443/v1": "provider.example.invalid:8443",
		"provider.example.invalid/v1?token=secret":               "provider.example.invalid",
		"/relative/path?token=secret":                            "",
		"://malformed-secret":                                    "",
	}
	for raw, expected := range tests {
		assert.Equal(t, expected, targetHost(raw), raw)
	}
}
