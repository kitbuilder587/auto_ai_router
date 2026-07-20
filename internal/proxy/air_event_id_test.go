package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mixaill76/auto_ai_router/internal/config"
	litellmdbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAIREventIDBindsResponseUpstreamHopAndSpendLog(t *testing.T) {
	for _, providerType := range []config.ProviderType{
		config.ProviderTypeOpenAI,
		config.ProviderTypeProxy,
	} {
		t.Run(string(providerType), func(t *testing.T) {
			upstreamHeaders := make(chan http.Header, 1)
			upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHeaders <- r.Header.Clone()
				w.Header().Add(airEventIDHeader, "upstream-event-spoof-1")
				w.Header().Add(strings.ToLower(airEventIDHeader), "upstream-event-spoof-2")
				w.Header().Set(shadowcontext.CallIDHeader, "upstream-call-spoof")
				w.Header().Set("X-Request-Id", "req_provider_fixed")
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(
					createMockChatCompletionResponse("chatcmpl-provider-fixed", "gpt-4", "ok"),
				)
			}))
			defer upstream.Close()

			sink := &recordingSpendSink{}
			prx := NewTestProxyBuilder().
				WithSingleCredential("air-event", providerType, upstream.URL, "upstream-key").
				Build()
			prx.spendLogger = sink

			request := newAIREventIDTestRequest()
			addAIREventIDSpoofs(request)
			response := httptest.NewRecorder()
			prx.ProxyRequest(response, request)

			require.Equal(t, http.StatusOK, response.Code)
			responseEventID := requireSingleAIREventID(t, response.Header())
			upstreamEventID := requireSingleAIREventID(t, <-upstreamHeaders)
			assert.Equal(t, responseEventID, upstreamEventID)
			assert.NotContains(t, responseEventID, "spoof")

			entries := sink.Entries()
			require.Len(t, entries, 1)
			assert.Equal(t, responseEventID, entries[0].AirEventID)
			assert.Equal(t, responseEventID, spendLogMetadataAirEventID(t, entries[0]))

			assert.Equal(t, "client-call-id", response.Header().Get(shadowcontext.CallIDHeader))
			assert.Equal(t, "req_provider_fixed", response.Header().Get("X-Request-Id"))
			var body struct {
				ID string `json:"id"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, "chatcmpl-provider-fixed", body.ID)
		})
	}
}

// TestAIREventIDIsHopLocalAcrossAIRRelay documents the relay boundary. A proxy
// credential forwards the outer AIR event ID to the adjacent hop, but the next
// AIR owns a fresh event ID for its provider attempts and SpendLog. Carrying an
// end-to-end ID through AIR relays would require a separately authenticated
// chain identity; X-Aar-Proxy-Client is deliberately insufficient.
func TestAIREventIDIsHopLocalAcrossAIRRelay(t *testing.T) {
	providerHeaders := make(chan http.Header, 1)
	provider := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(
			createMockChatCompletionResponse("chatcmpl-relay", "gpt-4", "ok"),
		)
	}))
	defer provider.Close()

	innerSink := &recordingSpendSink{}
	inner := NewTestProxyBuilder().
		WithSingleCredential("inner-provider", config.ProviderTypeOpenAI, provider.URL, "provider-key").
		Build()
	inner.spendLogger = innerSink

	relayIngressHeaders := make(chan http.Header, 1)
	innerServer := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayIngressHeaders <- r.Header.Clone()
		inner.ProxyRequest(w, r)
	}))
	defer innerServer.Close()

	outerSink := &recordingSpendSink{}
	outer := NewTestProxyBuilder().
		WithSingleCredential("inner-air", config.ProviderTypeProxy, innerServer.URL, "master-key").
		Build()
	outer.spendLogger = outerSink

	request := newAIREventIDTestRequest()
	addAIREventIDSpoofs(request)
	request.Header.Set("X-Aar-Proxy-Client", "1")
	response := httptest.NewRecorder()
	outer.ProxyRequest(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	outerEventID := requireSingleAIREventID(t, response.Header())
	assert.Equal(t, outerEventID, requireSingleAIREventID(t, <-relayIngressHeaders))

	innerEventID := requireSingleAIREventID(t, <-providerHeaders)
	assert.NotEqual(t, outerEventID, innerEventID)
	assert.NotContains(t, outerEventID, "spoof")
	assert.NotContains(t, innerEventID, "spoof")

	outerEntries := outerSink.Entries()
	require.Len(t, outerEntries, 1)
	assert.Equal(t, outerEventID, outerEntries[0].AirEventID)
	assert.Equal(t, outerEventID, spendLogMetadataAirEventID(t, outerEntries[0]))

	innerEntries := innerSink.Entries()
	require.Len(t, innerEntries, 1)
	assert.Equal(t, innerEventID, innerEntries[0].AirEventID)
	assert.Equal(t, innerEventID, spendLogMetadataAirEventID(t, innerEntries[0]))
}

func TestAIREventIDIsStableAcrossProviderRetries(t *testing.T) {
	var providerCalls atomic.Int32
	var providerIDsMu sync.Mutex
	providerIDs := make([]string, 0, 2)
	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerIDsMu.Lock()
		providerIDs = append(providerIDs, r.Header.Get(airEventIDHeader))
		providerIDsMu.Unlock()

		if providerCalls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"retry me","type":"server_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(
			createMockChatCompletionResponse("chatcmpl-after-retry", "gpt-4", "ok"),
		)
	}))
	defer upstream.Close()

	credentials := []config.CredentialConfig{
		{Name: "event-retry-1", Type: config.ProviderTypeOpenAI, BaseURL: upstream.URL, APIKey: "key-1", RPM: 100, TPM: 10000},
		{Name: "event-retry-2", Type: config.ProviderTypeOpenAI, BaseURL: upstream.URL, APIKey: "key-2", RPM: 100, TPM: 10000},
	}
	sink := &recordingSpendSink{}
	prx := NewTestProxyBuilder().
		WithCredentials(credentials...).
		WithMaxProviderRetries(1).
		Build()
	prx.spendLogger = sink

	response := httptest.NewRecorder()
	prx.ProxyRequest(response, newAIREventIDTestRequest())

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, int32(2), providerCalls.Load())
	responseEventID := requireSingleAIREventID(t, response.Header())
	providerIDsMu.Lock()
	observedProviderIDs := append([]string(nil), providerIDs...)
	providerIDsMu.Unlock()
	require.Equal(t, []string{responseEventID, responseEventID}, observedProviderIDs)
	entries := sink.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, responseEventID, entries[0].AirEventID)
	assert.Equal(t, responseEventID, spendLogMetadataAirEventID(t, entries[0]))
}

func TestConcurrentRequestsReceiveUniqueAIREventIDs(t *testing.T) {
	const requestCount = 4

	providerIDs := make(chan string, requestCount)
	releaseProviders := make(chan struct{})
	var arrived atomic.Int32
	var releaseOnce sync.Once
	upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerIDs <- r.Header.Get(airEventIDHeader)
		if arrived.Add(1) == requestCount {
			releaseOnce.Do(func() { close(releaseProviders) })
		}
		select {
		case <-releaseProviders:
		case <-time.After(5 * time.Second):
			http.Error(w, "concurrent request barrier timed out", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(
			createMockChatCompletionResponse("chatcmpl-concurrent-fixed", "gpt-4", "ok"),
		)
	}))
	defer upstream.Close()

	sink := &recordingSpendSink{}
	prx := NewTestProxyBuilder().
		WithSingleCredential("event-concurrent", config.ProviderTypeOpenAI, upstream.URL, "upstream-key").
		Build()
	prx.spendLogger = sink

	start := make(chan struct{})
	type capturedResponse struct {
		code    int
		headers http.Header
	}
	responses := make(chan capturedResponse, requestCount)
	var requests sync.WaitGroup
	for range requestCount {
		requests.Add(1)
		go func() {
			defer requests.Done()
			<-start
			response := httptest.NewRecorder()
			prx.ProxyRequest(response, newAIREventIDTestRequest())
			responses <- capturedResponse{code: response.Code, headers: response.Header().Clone()}
		}()
	}
	close(start)
	requests.Wait()
	close(responses)
	close(providerIDs)

	responseSet := make(map[string]struct{}, requestCount)
	for response := range responses {
		require.Equal(t, http.StatusOK, response.code)
		eventID := requireSingleAIREventID(t, response.headers)
		responseSet[eventID] = struct{}{}
	}
	require.Len(t, responseSet, requestCount)

	providerSet := make(map[string]struct{}, requestCount)
	for eventID := range providerIDs {
		providerSet[eventID] = struct{}{}
	}
	assert.Equal(t, responseSet, providerSet)

	entries := sink.Entries()
	require.Len(t, entries, requestCount)
	spendSet := make(map[string]struct{}, requestCount)
	for _, entry := range entries {
		require.Equal(t, entry.AirEventID, spendLogMetadataAirEventID(t, entry))
		spendSet[entry.AirEventID] = struct{}{}
	}
	assert.Equal(t, responseSet, spendSet)
}

func TestEarlyErrorsReturnFreshSingleAIREventID(t *testing.T) {
	prx := NewTestProxyBuilder().Build()
	tests := []struct {
		name       string
		authorize  bool
		body       string
		expectCode int
	}{
		{name: "authentication", body: `{"model":"gpt-4","messages":[]}`, expectCode: http.StatusUnauthorized},
		{name: "malformed JSON", authorize: true, body: `{`, expectCode: http.StatusBadRequest},
	}

	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			if tt.authorize {
				request.Header.Set("Authorization", "Bearer master-key")
			}
			addAIREventIDSpoofs(request)
			response := httptest.NewRecorder()

			prx.ProxyRequest(response, request)

			require.Equal(t, tt.expectCode, response.Code)
			eventID := requireSingleAIREventID(t, response.Header())
			assert.NotContains(t, eventID, "spoof")
			if _, duplicate := seen[eventID]; duplicate {
				t.Fatalf("AIR event ID was reused across early errors: %s", eventID)
			}
			seen[eventID] = struct{}{}
		})
	}
}

func newAIREventIDTestRequest() *http.Request {
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`),
	)
	request.Header.Set("Authorization", "Bearer master-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(shadowcontext.CallIDHeader, "client-call-id")
	return request
}

func addAIREventIDSpoofs(request *http.Request) {
	request.Header[airEventIDHeader] = []string{"client-event-spoof-1", "client-event-spoof-2"}
	request.Header[strings.ToLower(airEventIDHeader)] = []string{"client-event-spoof-3"}
	request.Header["X-VSELLM-Air-EVENT-ID"] = []string{"client-event-spoof-4"}
}

func requireSingleAIREventID(t *testing.T, headers http.Header) string {
	t.Helper()
	var names []string
	var values []string
	for name, headerValues := range headers {
		if strings.EqualFold(name, airEventIDHeader) {
			names = append(names, name)
			values = append(values, headerValues...)
		}
	}
	require.Len(t, names, 1, "AIR event ID must use one case-insensitive header field")
	require.Len(t, values, 1, "AIR event ID must have exactly one value")
	parsed, err := uuid.Parse(values[0])
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), parsed.Version())
	require.Equal(t, uuid.RFC4122, parsed.Variant())
	require.Equal(t, parsed.String(), values[0], "AIR event ID must use canonical UUID text")
	return values[0]
}

func spendLogMetadataAirEventID(t *testing.T, entry *litellmdbmodels.SpendLogEntry) string {
	t.Helper()
	var metadata struct {
		SpendLogsMetadata struct {
			AirEventID string `json:"air_event_id"`
		} `json:"spend_logs_metadata"`
	}
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	require.NotEmpty(t, metadata.SpendLogsMetadata.AirEventID)
	return metadata.SpendLogsMetadata.AirEventID
}
