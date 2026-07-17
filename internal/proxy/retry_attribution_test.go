package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFallbackSelectionKeepsResponseCredentialPairedAfterLaterTransportFailure(t *testing.T) {
	first := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Credential-Name", "nested-first")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"first fallback rate limited"}}`))
	}))
	defer first.Close()
	dead := newIPv4Server(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	firstCred := config.CredentialConfig{Name: "fallback-http", Type: config.ProviderTypeProxy, APIKey: "first", BaseURL: first.URL, RPM: 100, TPM: 1000, IsFallback: true}
	deadCred := config.CredentialConfig{Name: "fallback-dead", Type: config.ProviderTypeProxy, APIKey: "dead", BaseURL: deadURL, RPM: 100, TPM: 1000, IsFallback: true}
	events := []string{}
	sink := &keySpendTestSink{events: &events, commitSpend: 1}
	prx := NewTestProxyBuilder().WithCredentials(firstCred, deadCred).Build()
	prx.maxFallbackAttempts = 2
	prx.spendLogger = sink
	req := newKeySpendCommitTestRequest()
	originalCred := &config.CredentialConfig{Name: "primary", Type: config.ProviderTypeProxy, BaseURL: "https://primary.example.invalid"}
	logCtx := newKeySpendCommitLogContext(req, originalCred)
	w := httptest.NewRecorder()

	handled, reason := prx.TryFallbackProxy(
		w, req, "gpt-4", originalCred.Name, http.StatusTooManyRequests,
		RetryReasonRateLimit, []byte(`{"model":"gpt-4"}`), time.Now().UTC(), logCtx,
	)

	require.True(t, handled)
	assert.Empty(t, reason)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	require.NotNil(t, logCtx.Credential)
	assert.Equal(t, firstCred.Name, logCtx.Credential.Name)
	assert.Equal(t, firstCred.Name, logCtx.Billing.Credential())
	assert.Equal(t, targetHost(first.URL), logCtx.Billing.TargetHost())
	require.Len(t, logCtx.Billing.Attempts(), 2)
	assert.Equal(t, "provider_error", logCtx.Billing.Attempts()[0].Outcome)
	assert.Equal(t, "transport_error", logCtx.Billing.Attempts()[1].Outcome)
	require.Len(t, sink.committed, 1)
	assertSpendRoute(t, sink.committed[0].Metadata, "nested-first", targetHost(first.URL))
}

func TestAllFallbackTransportFailuresLeaveSelectedRouteUntouched(t *testing.T) {
	deadOne := newIPv4Server(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadOneURL := deadOne.URL
	deadOne.Close()
	deadTwo := newIPv4Server(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadTwoURL := deadTwo.URL
	deadTwo.Close()

	prx := NewTestProxyBuilder().WithCredentials(
		config.CredentialConfig{Name: "fallback-dead-1", Type: config.ProviderTypeProxy, BaseURL: deadOneURL, RPM: 100, TPM: 1000, IsFallback: true},
		config.CredentialConfig{Name: "fallback-dead-2", Type: config.ProviderTypeProxy, BaseURL: deadTwoURL, RPM: 100, TPM: 1000, IsFallback: true},
	).Build()
	prx.maxFallbackAttempts = 2
	req := newKeySpendCommitTestRequest()
	originalCred := &config.CredentialConfig{Name: "primary", Type: config.ProviderTypeProxy, BaseURL: "https://primary.example.invalid/v1"}
	logCtx := newKeySpendCommitLogContext(req, originalCred)
	w := httptest.NewRecorder()

	handled, _ := prx.TryFallbackProxy(
		w, req, "gpt-4", originalCred.Name, http.StatusBadGateway,
		RetryReasonNetErr, []byte(`{"model":"gpt-4"}`), time.Now().UTC(), logCtx,
	)

	assert.False(t, handled)
	assert.Equal(t, originalCred.Name, logCtx.Billing.Credential())
	assert.Equal(t, targetHost(originalCred.BaseURL), logCtx.Billing.TargetHost())
	require.NotNil(t, logCtx.Credential)
	assert.Equal(t, originalCred.Name, logCtx.Credential.Name)
	require.Len(t, logCtx.Billing.Attempts(), 2)
	for _, attempt := range logCtx.Billing.Attempts() {
		assert.Equal(t, "transport_error", attempt.Outcome)
	}
	assert.Zero(t, w.Body.Len())
}

func TestSameTypeProxyRetryAttributesSpendToRespondingCredential(t *testing.T) {
	first := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Credential-Name", "nested-first")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer first.Close()
	second := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","object":"chat.completion","model":"gpt-4","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer second.Close()

	firstCred := config.CredentialConfig{Name: "proxy-first", Type: config.ProviderTypeProxy, APIKey: "first", BaseURL: first.URL, RPM: 100, TPM: 1000}
	secondCred := config.CredentialConfig{Name: "proxy-second", Type: config.ProviderTypeProxy, APIKey: "second", BaseURL: second.URL, RPM: 100, TPM: 1000}
	events := []string{}
	sink := &keySpendTestSink{events: &events, commitSpend: 1}
	prx := NewTestProxyBuilder().WithCredentials(firstCred, secondCred).WithMaxProviderRetries(1).Build()
	prx.spendLogger = sink
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, newKeySpendCommitTestRequest())

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, sink.committed, 1)
	assertSpendRoute(t, sink.committed[0].Metadata, secondCred.Name, targetHost(second.URL))
}

func TestFallbackStreamFailureFinalizesFallbackIdentityBeforeLogging(t *testing.T) {
	tests := []struct {
		name        string
		writer      func() http.ResponseWriter
		stream      func() io.ReadCloser
		wantOutcome string
		wantHTTP    int
	}{
		{
			name:        "broken upstream",
			writer:      func() http.ResponseWriter { return httptest.NewRecorder() },
			stream:      func() io.ReadCloser { return io.NopCloser(&unexpectedEOFReader{}) },
			wantOutcome: "stream_error",
			wantHTTP:    http.StatusOK,
		},
		{
			name:   "client abort",
			writer: func() http.ResponseWriter { return newFailAfterNBytesWriter(0) },
			stream: func() io.ReadCloser {
				return io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: [DONE]\n\n"))
			},
			wantOutcome: "client_aborted",
			wantHTTP:    499,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fallback := &config.CredentialConfig{Name: "fallback-stream", Type: config.ProviderTypeProxy, BaseURL: "https://fallback.example.invalid/v1", RPM: 100, TPM: 1000, IsFallback: true}
			events := []string{}
			sink := &keySpendTestSink{events: &events}
			prx := NewTestProxyBuilder().WithCredentials(*fallback).Build()
			prx.spendLogger = sink
			req := newKeySpendCommitTestRequest()
			logCtx := newKeySpendCommitLogContext(req, &config.CredentialConfig{Name: "primary", Type: config.ProviderTypeProxy, BaseURL: "https://primary.example.invalid"})
			resp := &ProxyResponse{
				StatusCode:  http.StatusOK,
				Headers:     http.Header{"Content-Type": {"text/event-stream"}},
				StreamBody:  tt.stream(),
				IsStreaming: true,
			}

			handled, reason := prx.writeFallbackResponse(tt.writer(), req, resp, fallback, "gpt-4", "primary", logCtx, time.Now().UTC())

			require.True(t, handled)
			assert.Equal(t, "fallback_stream_write_failed", reason)
			assert.Equal(t, "failure", logCtx.Status)
			assert.Equal(t, tt.wantOutcome, logCtx.StreamOutcome)
			require.NotNil(t, logCtx.Credential)
			assert.Equal(t, fallback.Name, logCtx.Credential.Name)
			assert.Equal(t, fallback.Name, logCtx.Billing.Credential())
			assert.Equal(t, fallback.BaseURL, logCtx.TargetURL)
			assert.Equal(t, tt.wantHTTP, logCtx.HTTPStatus)
			assert.True(t, logCtx.Logged)
			require.Len(t, sink.replayed, 1)
			assert.Equal(t, "failure", sink.replayed[0].Status)
			assertSpendRoute(t, sink.replayed[0].Metadata, fallback.Name, targetHost(fallback.BaseURL))
		})
	}
}

func assertSpendRoute(t *testing.T, rawMetadata, wantCredential, wantHost string) {
	t.Helper()
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(rawMetadata), &metadata))
	extension, ok := metadata["spend_logs_metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, wantCredential, extension["actual_credential"])
	assert.Equal(t, wantHost, extension["actual_upstream_host"])
}
