package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	litellmdbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/spendsink"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/require"
)

const credentialSelectionKnownModel = "known-model"

type recordingSpendSink struct {
	mu          sync.Mutex
	entries     []*litellmdbmodels.SpendLogEntry
	logCalls    int
	commitCalls int
}

func (s *recordingSpendSink) LogSpend(entry *litellmdbmodels.SpendLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logCalls++
	s.entries = append(s.entries, entry)
	return nil
}

func (s *recordingSpendSink) CommitSpend(_ context.Context, entry *litellmdbmodels.SpendLogEntry) (spendsink.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitCalls++
	s.entries = append(s.entries, entry)
	return spendsink.CommitResult{}, nil
}

func (s *recordingSpendSink) ReadKeySpend(context.Context, string) (float64, bool, error) {
	return 0, false, nil
}

func (s *recordingSpendSink) IsEnabled() bool { return true }
func (s *recordingSpendSink) IsHealthy() bool { return true }
func (s *recordingSpendSink) Stats() litellmdbmodels.SpendLoggerStats {
	return litellmdbmodels.SpendLoggerStats{}
}
func (s *recordingSpendSink) Shutdown(context.Context) error { return nil }

func (s *recordingSpendSink) Entries() []*litellmdbmodels.SpendLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*litellmdbmodels.SpendLogEntry(nil), s.entries...)
}

func (s *recordingSpendSink) Calls() (logCalls, commitCalls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logCalls, s.commitCalls
}

func TestFinalizeStreamingLogWithSpendWriterUsesSynchronousCommit(t *testing.T) {
	credential := config.CredentialConfig{
		Name: "provider", Type: config.ProviderTypeOpenAI, BaseURL: "http://provider.invalid", APIKey: "provider-key",
	}
	prx := NewTestProxyBuilder().WithCredentials(credential).Build()
	sink := &recordingSpendSink{}
	prx.spendLogger = sink
	prx.spendLoggingRequired = true

	logCtx := &RequestLogContext{
		RequestID:     "stream-event",
		StartTime:     time.Now().Add(-time.Second),
		Request:       httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		PublicModelID: "public/chat",
		ModelID:       "backend-chat",
		RealModelID:   "backend-chat",
		Credential:    &credential,
	}

	prx.finalizeStreamingLog(logCtx, 2, nil, "openai", http.StatusOK)

	logCalls, commitCalls := sink.Calls()
	require.Zero(t, logCalls, "streaming spend must not depend on an unflushed async enqueue")
	require.Equal(t, 1, commitCalls, "streaming spend must synchronously commit or retain the exact event")
	require.True(t, logCtx.Logged)
}

func TestProxyRequestUnknownModelReturnsNotFoundWithoutSpendLog(t *testing.T) {
	prx, sink, _ := newCredentialSelectionTestProxy(t, 100)
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"unknown-model","messages":[{"role":"user","content":"hello"}]}`),
	)
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	testhelpers.AssertJSONErrorResponse(t, w, http.StatusNotFound, "not_found_error", "Model unknown-model not found")
	require.Empty(t, sink.Entries(), "unknown models must not create a zero-spend row")
}

func TestProxyRequestKnownUnavailableModelKeeps429AndSpendLogging(t *testing.T) {
	tests := []struct {
		name    string
		rpm     int
		arrange func(*testing.T, *Proxy, string)
		message string
	}{
		{
			name: "rate limited",
			rpm:  1,
			arrange: func(t *testing.T, prx *Proxy, credentialName string) {
				require.True(t, prx.rateLimiter.Allow(credentialName), "precondition: exhaust the credential RPM")
			},
			message: "Rate limit exceeded",
		},
		{
			name: "banned",
			rpm:  100,
			arrange: func(t *testing.T, prx *Proxy, credentialName string) {
				for range 3 {
					prx.balancer.RecordResponse(credentialName, credentialSelectionKnownModel, http.StatusInternalServerError)
				}
				require.True(t, prx.balancer.IsBanned(credentialName, credentialSelectionKnownModel))
			},
			message: "No credentials available for model known-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prx, sink, credentialName := newCredentialSelectionTestProxy(t, tt.rpm)
			tt.arrange(t, prx, credentialName)

			req := httptest.NewRequest(
				http.MethodPost,
				"/v1/chat/completions",
				strings.NewReader(`{"model":"known-model","messages":[{"role":"user","content":"hello"}]}`),
			)
			req.Header.Set("Authorization", "Bearer master-key")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			prx.ProxyRequest(w, req)

			testhelpers.AssertJSONErrorResponse(t, w, http.StatusTooManyRequests, "rate_limit_error", tt.message)
			entries := sink.Entries()
			require.Len(t, entries, 1, "known model selection failures keep the existing logging path")
			require.Equal(t, credentialSelectionKnownModel, entries[0].Model)
			require.Equal(t, "failure", entries[0].Status)
			require.Zero(t, entries[0].Spend)
		})
	}
}

func newCredentialSelectionTestProxy(t *testing.T, rpm int) (*Proxy, *recordingSpendSink, string) {
	t.Helper()
	logger := testhelpers.NewTestLogger()
	credential := config.CredentialConfig{
		Name:    "credential-selection-test",
		Type:    config.ProviderTypeOpenAI,
		BaseURL: "http://provider.invalid",
		APIKey:  "upstream-key",
		RPM:     rpm,
		TPM:     1000,
	}
	modelManager := models.New(logger, 50, []config.ModelRPMConfig{
		{Name: credentialSelectionKnownModel, Credential: credential.Name, RPM: 100},
	})
	modelManager.LoadModelsFromConfig([]config.CredentialConfig{credential})

	builder := NewTestProxyBuilder().
		WithCredentials(credential).
		WithMasterKey("master-key")
	builder.config.ModelManager = modelManager
	prx := builder.Build()
	sink := &recordingSpendSink{}
	prx.spendLogger = sink
	return prx, sink, credential.Name
}
