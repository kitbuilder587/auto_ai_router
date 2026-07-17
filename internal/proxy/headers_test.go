package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	dbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetHopByHopHeaders(t *testing.T) {
	headers := GetHopByHopHeaders()

	// Should contain all 8 RFC 7230 hop-by-hop headers
	expectedHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}

	assert.Len(t, headers, len(expectedHeaders))
	for _, h := range expectedHeaders {
		assert.True(t, headers[h], "should contain %s", h)
	}

	// Verify it returns a copy (modifying it doesn't affect the original)
	headers["X-Custom"] = true
	original := GetHopByHopHeaders()
	_, hasCustom := original["X-Custom"]
	assert.False(t, hasCustom, "modifying returned map should not affect the original")
}

func TestRequestHeaderCopyPathsStripAllClientCredentialVariants(t *testing.T) {
	source := httptest.NewRequest(http.MethodPost, "http://client.invalid/v1/chat/completions", nil)
	source.Header = http.Header{
		"Authorization": {"Bearer client-canonical", "Bearer client-duplicate"},
		"authorization": {"Bearer client-lowercase"},
		"X-Api-Key":     {"client-x-canonical"},
		"x-api-key":     {"client-x-lowercase"},
		"X-Custom":      {"preserved"},
	}

	t.Run("direct provider copy installs only provider credential", func(t *testing.T) {
		destination := httptest.NewRequest(http.MethodPost, "http://provider.invalid/v1/chat/completions", nil)
		copyRequestHeaders(destination, source, "provider-secret")

		assert.Equal(t, "Bearer provider-secret", destination.Header.Get("Authorization"))
		assert.Empty(t, destination.Header.Values("X-Api-Key"))
		assert.Equal(t, "preserved", destination.Header.Get("X-Custom"))
		for key, values := range destination.Header {
			assert.NotContains(t, strings.Join(values, " "), "client-", "client credential leaked through %s", key)
		}
	})

	t.Run("proxy copy strips credentials without replacement", func(t *testing.T) {
		destination := httptest.NewRequest(http.MethodPost, "http://proxy.invalid/v1/chat/completions", nil)
		copyHeadersSkipAuth(destination, source)

		assert.Empty(t, destination.Header.Values("Authorization"))
		assert.Empty(t, destination.Header.Values("X-Api-Key"))
		assert.Equal(t, "preserved", destination.Header.Get("X-Custom"))
		for key, values := range destination.Header {
			assert.NotContains(t, strings.Join(values, " "), "client-", "client credential leaked through %s", key)
		}
	})
}

func TestCopyResponseHeadersPreservesAIRCallID(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set(shadowcontext.CallIDHeader, "air-call-id")
	w.Header().Set(liteLLMKeySpendHeader, "1.25")
	w.Header().Set(liteLLMResponseCostHeader, "0.25")
	upstream := http.Header{}
	upstream.Add(shadowcontext.CallIDHeader, "untrusted-upstream-id")
	upstream.Add(liteLLMKeySpendHeader, "999.0")
	upstream.Add(liteLLMResponseCostHeader, "999.0")
	upstream.Add("X-Upstream", "kept")

	copyResponseHeaders(w, upstream, config.ProviderTypeOpenAI)

	assert.Equal(t, []string{"air-call-id"}, w.Header().Values(shadowcontext.CallIDHeader))
	assert.Equal(t, []string{"1.25"}, w.Header().Values(liteLLMKeySpendHeader))
	assert.Equal(t, []string{"0.25"}, w.Header().Values(liteLLMResponseCostHeader))
	assert.Equal(t, "kept", w.Header().Get("X-Upstream"))
}

func TestCopyResponseHeadersAliasesAnthropicRequestIDForOpenAIClients(t *testing.T) {
	w := httptest.NewRecorder()
	upstream := http.Header{"Request-Id": {"req_anthropic"}}

	copyResponseHeaders(w, upstream, config.ProviderTypeAnthropic)

	assert.Equal(t, "req_anthropic", w.Header().Get("Request-Id"))
	assert.Equal(t, "req_anthropic", w.Header().Get("X-Request-Id"))
}

func TestCopyResponseHeadersDoesNotOverwriteExplicitRequestID(t *testing.T) {
	w := httptest.NewRecorder()
	upstream := http.Header{
		"Request-Id":   {"req_anthropic"},
		"X-Request-Id": {"req_openai"},
	}

	copyResponseHeaders(w, upstream, config.ProviderTypeAnthropic)

	assert.Equal(t, "req_anthropic", w.Header().Get("Request-Id"))
	assert.Equal(t, "req_openai", w.Header().Get("X-Request-Id"))
}

func TestCopyResponseHeadersDoesNotAliasRequestIDForOpenAIProvider(t *testing.T) {
	w := httptest.NewRecorder()
	upstream := http.Header{"Request-Id": {"req_provider"}}

	copyResponseHeaders(w, upstream, config.ProviderTypeOpenAI)

	assert.Equal(t, "req_provider", w.Header().Get("Request-Id"))
	assert.Empty(t, w.Header().Get("X-Request-Id"))
}

func TestSetSuccessfulSSEHeaders(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		headers := http.Header{}
		setSuccessfulSSEHeaders(headers, http.StatusOK)
		assert.Equal(t, "no", headers.Get(accelBufferingHeader))
	})

	t.Run("pre-stream error", func(t *testing.T) {
		headers := http.Header{}
		setSuccessfulSSEHeaders(headers, http.StatusTooManyRequests)
		assert.Empty(t, headers.Get(accelBufferingHeader))
	})
}

func TestSetLiteLLMKeyHeadersUseCommittedSpendOnly(t *testing.T) {
	maxBudget := 1000.0
	rpmLimit := int64(100000)
	tpmLimit := int64(1000000)
	headers := http.Header{}

	setLiteLLMKeyLimitHeaders(headers, &dbmodels.TokenInfo{
		Spend:     5.85e-06,
		MaxBudget: &maxBudget,
		RPMLimit:  &rpmLimit,
		TPMLimit:  &tpmLimit,
	})
	setLiteLLMKeySpendHeader(headers, 7.35e-06, true)

	assert.Equal(t, "7.35e-06", headers.Get(liteLLMKeySpendHeader))
	assert.Equal(t, "1000.0", headers.Get(liteLLMKeyMaxBudgetHeader))
	assert.Equal(t, "100000", headers.Get(liteLLMKeyRPMLimitHeader))
	assert.Equal(t, "1000000", headers.Get(liteLLMKeyTPMLimitHeader))
}

func TestSetLiteLLMKeyHeadersOmitUnknownState(t *testing.T) {
	headers := http.Header{}
	setLiteLLMKeyLimitHeaders(headers, &dbmodels.TokenInfo{})
	setLiteLLMKeySpendHeader(headers, 0, false)

	assert.Empty(t, headers.Get(liteLLMKeySpendHeader))
	assert.Empty(t, headers.Get(liteLLMKeyMaxBudgetHeader))
	assert.Empty(t, headers.Get(liteLLMKeyRPMLimitHeader))
	assert.Empty(t, headers.Get(liteLLMKeyTPMLimitHeader))
}

func TestSetLiteLLMKeySpendHeaderRemovesEarlierSnapshotWhenCommitIsUnknown(t *testing.T) {
	headers := http.Header{liteLLMKeySpendHeader: {"12.0"}}

	setLiteLLMKeySpendHeader(headers, 0, false)

	assert.Empty(t, headers.Get(liteLLMKeySpendHeader))
}

func TestSetLiteLLMResponseCostHeaderDoesNotExposeInternalBreakdown(t *testing.T) {
	headers := http.Header{}
	setLiteLLMResponseCostHeader(headers, 7.349999999999999e-06)

	assert.Equal(t, "7.349999999999999e-06", headers.Get(liteLLMResponseCostHeader))
	assert.Empty(t, headers.Get(liteLLMResponseCostOriginalHeader))
	assert.Empty(t, headers.Get(liteLLMResponseCostDiscountHeader))
	assert.Empty(t, headers.Get(liteLLMResponseCostMarginAmountHeader))
	assert.Empty(t, headers.Get(liteLLMResponseCostMarginPercentHeader))
}

func TestSetLiteLLMResponseCostHeaderForError(t *testing.T) {
	headers := http.Header{}
	setLiteLLMResponseCostHeader(headers, 0)

	assert.Equal(t, "0", headers.Get(liteLLMResponseCostHeader))
	assert.Empty(t, headers.Get(liteLLMResponseCostOriginalHeader))
	assert.Empty(t, headers.Get(liteLLMResponseCostDiscountHeader))
	assert.Empty(t, headers.Get(liteLLMResponseCostMarginAmountHeader))
	assert.Empty(t, headers.Get(liteLLMResponseCostMarginPercentHeader))
}

func TestLiteLLMFinancialHeadersAreOwnedByOutermostGateway(t *testing.T) {
	tests := []struct {
		name   string
		logCtx *RequestLogContext
		want   bool
	}{
		{name: "nil direct context", want: true},
		{name: "missing signed context", logCtx: &RequestLogContext{ShadowContext: shadowcontext.Result{State: shadowcontext.StateMissing}}, want: true},
		{name: "invalid signed context", logCtx: &RequestLogContext{ShadowContext: shadowcontext.Result{State: shadowcontext.StateInvalid}}, want: true},
		{name: "valid signed context", logCtx: &RequestLogContext{ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldExposeLiteLLMFinancialHeaders(tt.logCtx))
		})
	}
}

func TestValidSignedContextSuppressesOnlyLiteLLMFinancialHeaders(t *testing.T) {
	maxBudget := 1000.0
	rpmLimit := int64(100000)
	tpmLimit := int64(1000000)
	logCtx := &RequestLogContext{ShadowContext: shadowcontext.Result{State: shadowcontext.StateValid}}
	headers := http.Header{
		shadowcontext.CallIDHeader:             {"call-1"},
		accelBufferingHeader:                   {"no"},
		liteLLMKeySpendHeader:                  {"stale"},
		liteLLMKeyMaxBudgetHeader:              {"stale"},
		liteLLMKeyRPMLimitHeader:               {"stale"},
		liteLLMKeyTPMLimitHeader:               {"stale"},
		liteLLMResponseCostHeader:              {"stale"},
		liteLLMResponseCostOriginalHeader:      {"stale"},
		liteLLMResponseCostDiscountHeader:      {"stale"},
		liteLLMResponseCostMarginAmountHeader:  {"stale"},
		liteLLMResponseCostMarginPercentHeader: {"stale"},
	}

	setLiteLLMKeyLimitHeadersForRequest(headers, &dbmodels.TokenInfo{
		MaxBudget: &maxBudget,
		RPMLimit:  &rpmLimit,
		TPMLimit:  &tpmLimit,
	}, logCtx)
	setLiteLLMKeySpendHeaderForRequest(headers, 12, true, logCtx)
	setLiteLLMResponseCostHeaderForRequest(headers, 0.25, logCtx)

	for _, name := range []string{
		liteLLMKeySpendHeader,
		liteLLMKeyMaxBudgetHeader,
		liteLLMKeyRPMLimitHeader,
		liteLLMKeyTPMLimitHeader,
		liteLLMResponseCostHeader,
		liteLLMResponseCostOriginalHeader,
		liteLLMResponseCostDiscountHeader,
		liteLLMResponseCostMarginAmountHeader,
		liteLLMResponseCostMarginPercentHeader,
	} {
		assert.Empty(t, headers.Get(name), "%s must be owned by outer LiteLLM", name)
	}
	assert.Equal(t, "call-1", headers.Get(shadowcontext.CallIDHeader))
	assert.Equal(t, "no", headers.Get(accelBufferingHeader))
}

func TestMissingAndInvalidSignedContextsRetainDirectAIRFinancialHeaders(t *testing.T) {
	for _, state := range []shadowcontext.State{shadowcontext.StateMissing, shadowcontext.StateInvalid} {
		t.Run(string(state), func(t *testing.T) {
			maxBudget := 1000.0
			rpmLimit := int64(100000)
			tpmLimit := int64(1000000)
			logCtx := &RequestLogContext{ShadowContext: shadowcontext.Result{State: state}}
			headers := http.Header{}

			setLiteLLMKeyLimitHeadersForRequest(headers, &dbmodels.TokenInfo{
				MaxBudget: &maxBudget,
				RPMLimit:  &rpmLimit,
				TPMLimit:  &tpmLimit,
			}, logCtx)
			setLiteLLMKeySpendHeaderForRequest(headers, 12, true, logCtx)
			setLiteLLMResponseCostHeaderForRequest(headers, 0.25, logCtx)

			assert.Equal(t, "12.0", headers.Get(liteLLMKeySpendHeader))
			assert.Equal(t, "1000.0", headers.Get(liteLLMKeyMaxBudgetHeader))
			assert.Equal(t, "100000", headers.Get(liteLLMKeyRPMLimitHeader))
			assert.Equal(t, "1000000", headers.Get(liteLLMKeyTPMLimitHeader))
			assert.Equal(t, "0.25", headers.Get(liteLLMResponseCostHeader))
		})
	}
}

func TestSignedLiteLLMChainDoesNotReturnNestedFinancialHeaders(t *testing.T) {
	t.Run("non-stream", func(t *testing.T) {
		events := []string{}
		sink := &keySpendTestSink{events: &events, readSpend: 10, commitSpend: 12}
		prx, upstream := newKeySpendCommitTestProxy(t, sink, config.ProviderTypeProxy)
		defer upstream.Close()
		verifier, privateKey := proxyTestVerifier(t)
		prx.shadowContextVerifier = verifier
		req := newKeySpendCommitTestRequest()
		req.Header.Set(shadowcontext.AuthContextHeader, proxySignContext(t, privateKey, "outer-call", "financial-non-stream"))
		req.Header.Set(shadowcontext.CallIDHeader, "outer-call")
		w := httptest.NewRecorder()

		prx.ProxyRequest(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, []string{"commit"}, events, "billing must continue even though nested headers are suppressed")
		assert.Equal(t, "outer-call", w.Header().Get(shadowcontext.CallIDHeader))
		assertLiteLLMFinancialHeadersEmpty(t, w.Header())
	})

	t.Run("retained commit failure", func(t *testing.T) {
		events := []string{}
		sink := &keySpendTestSink{
			events: &events, readSpend: 10,
			commitErr: errors.New("ambiguous commit"), commitReplayRetained: true,
		}
		prx, upstream := newKeySpendCommitTestProxy(t, sink, config.ProviderTypeProxy)
		defer upstream.Close()
		verifier, privateKey := proxyTestVerifier(t)
		prx.shadowContextVerifier = verifier
		req := newKeySpendCommitTestRequest()
		req.Header.Set(shadowcontext.AuthContextHeader, proxySignContext(t, privateKey, "outer-failed-call", "financial-retained-failure"))
		req.Header.Set(shadowcontext.CallIDHeader, "outer-failed-call")
		w := httptest.NewRecorder()

		prx.ProxyRequest(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, []string{"commit"}, events,
			"a signed nested request must neither read nor expose a local spend snapshot")
		require.Len(t, sink.retained, 1)
		assertLiteLLMFinancialHeadersEmpty(t, w.Header())
	})

	t.Run("stream", func(t *testing.T) {
		events := []string{}
		sink := &keySpendTestSink{events: &events, readSpend: 10, commitSpend: 12}
		upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer upstream.Close()
		prx := NewTestProxyBuilder().
			WithSingleCredential("proxy-upstream", config.ProviderTypeProxy, upstream.URL, "upstream-key").
			WithMasterKey("master-key").
			Build()
		prx.spendLogger = sink
		verifier, privateKey := proxyTestVerifier(t)
		prx.shadowContextVerifier = verifier
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
			`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"stream":true}`,
		))
		req.Header.Set("Authorization", "Bearer master-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(shadowcontext.AuthContextHeader, proxySignContext(t, privateKey, "outer-stream-call", "financial-stream"))
		req.Header.Set(shadowcontext.CallIDHeader, "outer-stream-call")
		w := httptest.NewRecorder()

		prx.ProxyRequest(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, []string{"async-replay"}, events, "stream billing must continue without a nested spend snapshot")
		assert.Equal(t, "outer-stream-call", w.Header().Get(shadowcontext.CallIDHeader))
		assertLiteLLMFinancialHeadersEmpty(t, w.Header())
	})
}

func assertLiteLLMFinancialHeadersEmpty(t *testing.T, headers http.Header) {
	t.Helper()
	for _, name := range []string{
		liteLLMKeySpendHeader,
		liteLLMKeyMaxBudgetHeader,
		liteLLMKeyRPMLimitHeader,
		liteLLMKeyTPMLimitHeader,
		liteLLMResponseCostHeader,
		liteLLMResponseCostOriginalHeader,
		liteLLMResponseCostDiscountHeader,
		liteLLMResponseCostMarginAmountHeader,
		liteLLMResponseCostMarginPercentHeader,
	} {
		assert.Empty(t, headers.Get(name), "%s must be owned by outer LiteLLM", name)
	}
}
