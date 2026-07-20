package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb"
	litellmdbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/mixaill76/auto_ai_router/internal/spendsink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type keySpendTestSink struct {
	events               *[]string
	commitErr            error
	logErr               error
	blockCommit          bool
	blockRead            bool
	readUnknown          bool
	commitReplayRetained bool
	committed            []*litellmdbmodels.SpendLogEntry
	replayed             []*litellmdbmodels.SpendLogEntry
	retained             []*litellmdbmodels.SpendLogEntry
	readKey              string
	readSpend            float64
	commitSpend          float64
	readDeadline         time.Time
	commitDeadline       time.Time
}

func (s *keySpendTestSink) LogSpend(entry *litellmdbmodels.SpendLogEntry) error {
	*s.events = append(*s.events, "async-replay")
	if s.logErr != nil {
		return s.logErr
	}
	s.replayed = append(s.replayed, entry)
	return nil
}

func (s *keySpendTestSink) CommitSpend(ctx context.Context, entry *litellmdbmodels.SpendLogEntry) (spendsink.CommitResult, error) {
	*s.events = append(*s.events, "commit")
	s.committed = append(s.committed, entry)
	if deadline, ok := ctx.Deadline(); ok {
		s.commitDeadline = deadline
	}
	if s.blockCommit {
		<-ctx.Done()
		if s.commitReplayRetained {
			s.retained = append(s.retained, entry)
		}
		return spendsink.CommitResult{ReplayRetained: s.commitReplayRetained}, ctx.Err()
	}
	if s.commitErr != nil {
		if s.commitReplayRetained {
			s.retained = append(s.retained, entry)
		}
		return spendsink.CommitResult{ReplayRetained: s.commitReplayRetained}, s.commitErr
	}
	return spendsink.CommitResult{Inserted: true, EffectiveRequestID: entry.RequestID, KeySpend: s.commitSpend, KeySpendKnown: true}, nil
}

func (s *keySpendTestSink) ReadKeySpend(ctx context.Context, apiKeyHash string) (float64, bool, error) {
	*s.events = append(*s.events, "read")
	s.readKey = apiKeyHash
	if deadline, ok := ctx.Deadline(); ok {
		s.readDeadline = deadline
	}
	if s.blockRead {
		<-ctx.Done()
		return 0, false, ctx.Err()
	}
	if s.readUnknown {
		return 0, false, nil
	}
	return s.readSpend, true, nil
}

func (s *keySpendTestSink) IsEnabled() bool { return true }
func (s *keySpendTestSink) IsHealthy() bool { return true }
func (s *keySpendTestSink) Stats() litellmdbmodels.SpendLoggerStats {
	return litellmdbmodels.SpendLoggerStats{}
}
func (s *keySpendTestSink) Shutdown(context.Context) error { return nil }

type keySpendEventWriter struct {
	*httptest.ResponseRecorder
	events *[]string
	wrote  bool
}

func (w *keySpendEventWriter) recordWrite() {
	if w.wrote {
		return
	}
	w.wrote = true
	*w.events = append(*w.events, "write")
}

func (w *keySpendEventWriter) WriteHeader(statusCode int) {
	w.recordWrite()
	w.ResponseRecorder.WriteHeader(statusCode)
}

func (w *keySpendEventWriter) Write(body []byte) (int, error) {
	w.recordWrite()
	return w.ResponseRecorder.Write(body)
}

func TestProxyNonStreamCommitsSpendBeforeWritingResponse(t *testing.T) {
	for _, providerType := range []config.ProviderType{config.ProviderTypeProxy, config.ProviderTypeOpenAI} {
		t.Run(string(providerType), func(t *testing.T) {
			events := []string{}
			sink := &keySpendTestSink{events: &events, readSpend: 10, commitSpend: 12}
			prx, upstream := newKeySpendCommitTestProxy(t, sink, providerType)
			defer upstream.Close()
			w := &keySpendEventWriter{ResponseRecorder: httptest.NewRecorder(), events: &events}

			prx.ProxyRequest(w, newKeySpendCommitTestRequest())

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, []string{"read", "commit", "write"}, events)
			assert.Equal(t, "12.0", w.Header().Get(liteLLMKeySpendHeader))
			require.Len(t, sink.committed, 1)
			assert.Equal(t, litellmdb.HashToken("master-key"), sink.readKey)
			assert.Equal(t, sink.readKey, sink.committed[0].APIKey)
			assert.Empty(t, sink.replayed)
			assertSpendDeadline(t, sink.readDeadline)
			assertSpendDeadline(t, sink.commitDeadline)
		})
	}
}

func TestProxyNonStreamCommitFailureOmitsSpendAndQueuesSameEventBeforeResponse(t *testing.T) {
	events := []string{}
	sink := &keySpendTestSink{
		events:      &events,
		readSpend:   10,
		commitSpend: 12,
		commitErr:   errors.New("ambiguous commit"),
	}
	prx, upstream := newKeySpendCommitTestProxy(t, sink, config.ProviderTypeProxy)
	defer upstream.Close()
	w := &keySpendEventWriter{ResponseRecorder: httptest.NewRecorder(), events: &events}

	prx.ProxyRequest(w, newKeySpendCommitTestRequest())

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"read", "commit", "async-replay", "write"}, events)
	assert.Empty(t, w.Header().Get(liteLLMKeySpendHeader))
	require.Len(t, sink.committed, 1)
	require.Len(t, sink.replayed, 1)
	assert.Same(t, sink.committed[0], sink.replayed[0], "replay must preserve every idempotency identifier")
}

func TestProxyStreamUsesPreRequestCommittedSnapshotAndAsyncFinalization(t *testing.T) {
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
	w := &keySpendEventWriter{ResponseRecorder: httptest.NewRecorder(), events: &events}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"stream":true}`,
	))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")

	prx.ProxyRequest(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"read", "write", "async-replay"}, events)
	assert.Equal(t, "10.0", w.Header().Get(liteLLMKeySpendHeader))
	assert.Empty(t, sink.committed)
	require.Len(t, sink.replayed, 1)
}

func TestFallbackNonStreamCommitsSpendBeforeWritingResponse(t *testing.T) {
	events := []string{}
	sink := &keySpendTestSink{events: &events, commitSpend: 12}
	fallback := &config.CredentialConfig{
		Name: "fallback", Type: config.ProviderTypeProxy, BaseURL: "http://fallback.invalid", RPM: 100, TPM: 1000, IsFallback: true,
	}
	prx := NewTestProxyBuilder().WithCredentials(*fallback).Build()
	prx.spendLogger = sink
	req := newKeySpendCommitTestRequest()
	startedAt := time.Now().UTC()
	logCtx := &RequestLogContext{
		RequestID:     "fallback-event",
		CallID:        "fallback-call",
		StartTime:     startedAt,
		Request:       req,
		Token:         "master-key",
		TokenInfo:     &litellmdbmodels.TokenInfo{Token: litellmdb.HashToken("master-key")},
		PublicModelID: "gpt-4",
		ModelID:       "gpt-4",
		RealModelID:   "gpt-4",
	}
	logCtx.Billing = NewBillingContext(logCtx.RequestID, logCtx.CallID, req.URL.Path, shadowcontext.Identity{}).
		WithPublicModel("gpt-4").
		WithRouting("gpt-4", "gpt-4", string(config.ProviderTypeProxy), fallback.Name, fallback.BaseURL)
	proxyResp := &ProxyResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"id":"chatcmpl-fallback","object":"chat.completion","model":"gpt-4","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`),
	}
	w := &keySpendEventWriter{ResponseRecorder: httptest.NewRecorder(), events: &events}

	handled, reason := prx.writeFallbackResponse(w, req, proxyResp, fallback, "gpt-4", "primary", logCtx, startedAt)

	require.True(t, handled)
	assert.Empty(t, reason)
	assert.Equal(t, []string{"commit", "write"}, events)
	assert.Equal(t, "12.0", w.Header().Get(liteLLMKeySpendHeader))
	require.Len(t, sink.committed, 1)
	assert.Empty(t, sink.replayed)
}

func TestSpendIdentityKeyPrefersSignedTenantOverAIRMasterKey(t *testing.T) {
	logCtx := &RequestLogContext{
		Token:     "master-key",
		TokenInfo: &litellmdbmodels.TokenInfo{Token: litellmdb.HashToken("master-key")},
		ShadowContext: shadowcontext.Result{
			State:    shadowcontext.StateValid,
			Identity: shadowcontext.Identity{APIKeyHash: "tenant-client-hash"},
		},
	}

	assert.Equal(t, "tenant-client-hash", spendIdentityKey(logCtx))
}

func TestProxyNonStreamCommitDeadlineUsesKnownPostgreSQLSnapshotWithExactRetention(t *testing.T) {
	events := []string{}
	sink := &keySpendTestSink{
		events: &events, readSpend: 10, commitSpend: 999,
		blockCommit: true, commitReplayRetained: true,
	}
	prx, upstream := newKeySpendCommitTestProxy(t, sink, config.ProviderTypeProxy)
	defer upstream.Close()
	w := &keySpendEventWriter{ResponseRecorder: httptest.NewRecorder(), events: &events}

	requestCtx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	req := newKeySpendCommitTestRequest().WithContext(requestCtx)
	started := time.Now()
	prx.ProxyRequest(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Less(t, time.Since(started), time.Second, "accounting timeout must fail open before the response")
	assert.Equal(t, []string{"read", "commit", "write"}, events)
	assert.Equal(t, "10.0", w.Header().Get(liteLLMKeySpendHeader),
		"timeout fallback must use only the pre-provider PostgreSQL snapshot")
	require.Len(t, sink.committed, 1)
	assert.Empty(t, sink.replayed, "lifecycle-owned retention must not enqueue a second replay")
	require.Len(t, sink.retained, 1)
	assert.Same(t, sink.committed[0], sink.retained[0], "timeout retention must preserve the exact event object and IDs")
	assert.NotEqual(t, "999.0", w.Header().Get(liteLLMKeySpendHeader),
		"an unacknowledged inclusive value must never be fabricated")
}

func TestProxyRetainedCommitFailureCannotInventUnknownSnapshot(t *testing.T) {
	events := []string{}
	sink := &keySpendTestSink{
		events: &events, readUnknown: true,
		commitErr: errors.New("ambiguous commit"), commitReplayRetained: true,
	}
	prx, upstream := newKeySpendCommitTestProxy(t, sink, config.ProviderTypeProxy)
	defer upstream.Close()
	w := &keySpendEventWriter{ResponseRecorder: httptest.NewRecorder(), events: &events}

	prx.ProxyRequest(w, newKeySpendCommitTestRequest())

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"read", "commit", "write"}, events)
	assert.Empty(t, w.Header().Get(liteLLMKeySpendHeader),
		"exact replay retention cannot turn an unknown database value into a header")
	require.Len(t, sink.retained, 1)
}

func TestCommitAndReplayEnqueueFailureRemainTruthfullyUnlogged(t *testing.T) {
	events := []string{}
	sink := &keySpendTestSink{
		events:    &events,
		commitErr: errors.New("ambiguous commit"),
		logErr:    errors.New("queue full"),
	}
	prx := NewTestProxyBuilder().Build()
	prx.spendLogger = sink
	req := newKeySpendCommitTestRequest()
	cred := &config.CredentialConfig{
		Name: "primary", Type: config.ProviderTypeProxy, BaseURL: "https://provider.example.invalid/v1",
	}
	logCtx := newKeySpendCommitLogContext(req, cred)
	headers := make(http.Header)

	result, err := prx.commitSpendBeforeResponse(context.Background(), headers, logCtx)

	require.Error(t, err)
	assert.Equal(t, spendReplayEnqueueFailed, result.Disposition)
	assert.False(t, logCtx.Logged)
	require.Len(t, sink.committed, 1)
	assert.Same(t, sink.committed[0], logCtx.pendingSpendEntry)
	assert.Empty(t, sink.replayed)
	assert.Empty(t, headers.Get(liteLLMKeySpendHeader))

	err = prx.finalizeDeferredSpend(logCtx)
	require.Error(t, err)
	assert.False(t, logCtx.Logged, "a rejected replay must not be reported as logged")
	assert.Same(t, sink.committed[0], logCtx.pendingSpendEntry)

	sink.logErr = nil
	require.NoError(t, prx.finalizeDeferredSpend(logCtx))
	assert.True(t, logCtx.Logged)
	assert.Nil(t, logCtx.pendingSpendEntry)
	require.Len(t, sink.replayed, 1)
	assert.Same(t, sink.committed[0], sink.replayed[0])
}

func assertSpendDeadline(t *testing.T, deadline time.Time) {
	t.Helper()
	require.False(t, deadline.IsZero(), "response-path accounting must carry a dedicated deadline")
	remaining := time.Until(deadline)
	assert.Greater(t, remaining, time.Duration(0))
	assert.LessOrEqual(t, remaining, spendResponseBudget)
}

func newKeySpendCommitLogContext(req *http.Request, cred *config.CredentialConfig) *RequestLogContext {
	startedAt := time.Now().UTC().Add(-time.Millisecond)
	logCtx := &RequestLogContext{
		RequestID:     "key-spend-event",
		CallID:        "key-spend-call",
		StartTime:     startedAt,
		Request:       req,
		Token:         "master-key",
		TokenInfo:     &litellmdbmodels.TokenInfo{Token: litellmdb.HashToken("master-key")},
		PublicModelID: "gpt-4",
		ModelID:       "gpt-4",
		RealModelID:   "gpt-4",
		Credential:    cred,
		TargetURL:     cred.BaseURL,
		Status:        "success",
		HTTPStatus:    http.StatusOK,
	}
	logCtx.Billing = NewBillingContext(logCtx.RequestID, logCtx.CallID, req.URL.Path, shadowcontext.Identity{}).
		WithPublicModel("gpt-4").
		WithRouting("gpt-4", "gpt-4", string(cred.Type), cred.Name, cred.BaseURL)
	return logCtx
}

func newKeySpendCommitTestProxy(t *testing.T, sink spendsink.Sink, providerType config.ProviderType) (*Proxy, *httptest.Server) {
	t.Helper()
	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-key-spend","object":"chat.completion","created":1,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	prx := NewTestProxyBuilder().
		WithSingleCredential("proxy-upstream", providerType, upstream.URL, "upstream-key").
		WithMasterKey("master-key").
		Build()
	prx.spendLogger = sink
	return prx, upstream
}

func newKeySpendCommitTestRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`,
	))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	return req
}
