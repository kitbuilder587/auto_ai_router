package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
)

func validChatRequest() string {
	return `{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}`
}

func TestProxyRequestRejectsMalformedSuccessfulProviderJSON(t *testing.T) {
	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":`))
	}))
	defer upstream.Close()

	proxy := NewTestProxyBuilder().
		WithSingleCredential("openai", config.ProviderTypeOpenAI, upstream.URL, "upstream-key").
		WithMaxProviderRetries(0).
		Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validChatRequest()))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	proxy.ProxyRequest(recorder, req)

	testhelpers.AssertJSONErrorResponse(t, recorder, http.StatusBadGateway, "api_connection_error", "Upstream provider returned an invalid JSON response")
}

func TestProxyRequestReturnsGatewayTimeoutForProviderTimeout(t *testing.T) {
	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"late"}`))
	}))
	defer upstream.Close()

	proxy := NewTestProxyBuilder().
		WithSingleCredential("openai", config.ProviderTypeOpenAI, upstream.URL, "upstream-key").
		WithRequestTimeout(20 * time.Millisecond).
		WithMaxProviderRetries(0).
		Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validChatRequest()))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	proxy.ProxyRequest(recorder, req)

	testhelpers.AssertJSONErrorResponse(t, recorder, http.StatusGatewayTimeout, "timeout_error", "Gateway Timeout")
}

func TestProxyRequestNormalizesAnthropicProviderError(t *testing.T) {
	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"provider overloaded"}}`))
	}))
	defer upstream.Close()

	proxy := NewTestProxyBuilder().
		WithSingleCredential("anthropic", config.ProviderTypeAnthropic, upstream.URL, "upstream-key").
		WithMaxProviderRetries(0).
		Build()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validChatRequest()))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	proxy.ProxyRequest(recorder, req)

	testhelpers.AssertJSONErrorResponse(t, recorder, 529, "overloaded_error", "provider overloaded")
}
