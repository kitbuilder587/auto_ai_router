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
	litellmdbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExhaustedOpenAIChatRetriesPersistCompletionCallType(t *testing.T) {
	var providerCalls atomic.Int32
	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable","type":"server_error"}}`))
	}))
	defer upstream.Close()

	credentials := make([]config.CredentialConfig, 5)
	for index := range credentials {
		credentials[index] = config.CredentialConfig{
			Name:    "openai-retry-" + string(rune('1'+index)),
			Type:    config.ProviderTypeOpenAI,
			BaseURL: upstream.URL,
			APIKey:  "upstream-key",
			RPM:     100,
			TPM:     10000,
		}
	}
	sink := &recordingShadowSpendSink{}
	proxy := NewTestProxyBuilder().
		WithCredentials(credentials...).
		WithMaxProviderRetries(4).
		Build()
	proxy.spendLogger = sink

	request := openAIChatFailureRequest(false)
	response := httptest.NewRecorder()
	proxy.ProxyRequest(response, request)

	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.Equal(t, int32(5), providerCalls.Load())
	entries := sink.Entries()
	require.Len(t, entries, 1)
	assertOpenAIChatFailurePersistence(t, entries[0], 5)
}

func TestPreStreamOpenAI429PersistsCompletionCallType(t *testing.T) {
	var providerCalls atomic.Int32
	providerBodies := make(chan string, 1)
	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		body, _ := io.ReadAll(request.Body)
		providerBodies <- string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited before stream","type":"rate_limit_error"}}`))
	}))
	defer upstream.Close()

	sink := &recordingShadowSpendSink{}
	proxy := NewTestProxyBuilder().
		WithSingleCredential("openai-pre-stream", config.ProviderTypeOpenAI, upstream.URL, "upstream-key").
		WithMaxProviderRetries(0).
		Build()
	proxy.spendLogger = sink

	request := openAIChatFailureRequest(true)
	response := httptest.NewRecorder()
	proxy.ProxyRequest(response, request)

	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.Equal(t, int32(1), providerCalls.Load())
	assert.Contains(t, <-providerBodies, `"stream":true`)
	entries := sink.Entries()
	require.Len(t, entries, 1)
	assertOpenAIChatFailurePersistence(t, entries[0], 1)
}

func openAIChatFailureRequest(stream bool) *http.Request {
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]`
	if stream {
		body += `,"stream":true`
	}
	body += `}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer master-key")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func assertOpenAIChatFailurePersistence(t *testing.T, entry *litellmdbmodels.SpendLogEntry, expectedAttempts int) {
	t.Helper()
	assert.Equal(t, "failure", entry.Status)
	assert.Equal(t, "acompletion", entry.CallType)

	var metadata struct {
		SpendLogsMetadata struct {
			OriginalCallType string `json:"original_call_type"`
			Attempts         []any  `json:"attempts"`
		} `json:"spend_logs_metadata"`
	}
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	assert.Equal(t, "acompletion", metadata.SpendLogsMetadata.OriginalCallType)
	assert.Len(t, metadata.SpendLogsMetadata.Attempts, expectedAttempts)
}

func TestFailureCallTypePreservationIsRouteBasedAndChatScoped(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		provider string
		want     string
	}{
		{name: "OpenAI-backed chat", endpoint: "/v1/chat/completions", provider: "openai", want: "acompletion"},
		{name: "Anthropic-backed OpenAI chat", endpoint: "/v1/chat/completions", provider: "anthropic", want: "acompletion"},
		{name: "OpenAI embeddings remain out of scope", endpoint: "/v1/embeddings", provider: "openai"},
		{name: "OpenAI image generation remains out of scope", endpoint: "/v1/images/generations", provider: "openai"},
		{name: "native Anthropic remains out of scope", endpoint: "/v1/messages", provider: "anthropic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			billing := NewBillingContext("event", "call", tt.endpoint, shadowcontext.Identity{}).
				WithRouting("backend", "provider-model", tt.provider, "credential", "https://provider.invalid")
			assert.Equal(t, tt.want, spendLogCallType("failure", billing, false))
		})
	}
}

func TestFailureWithMaterialEffectKeepsEveryCanonicalRoute(t *testing.T) {
	for _, endpoint := range []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/embeddings",
		"/v1/responses",
		"/v1/images/generations",
		"/v1/images/edits",
	} {
		t.Run(endpoint, func(t *testing.T) {
			billing := NewBillingContext("event", "call", endpoint, shadowcontext.Identity{})
			assert.NotEmpty(t, spendLogCallType("failure", billing, true))
		})
	}
}
