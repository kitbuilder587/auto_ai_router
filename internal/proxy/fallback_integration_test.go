package proxy

// Integration Tests for Fallback Proxy Chain Mechanism
//
// This test file validates the fallback proxy mechanism when primary credential fails.
// It tests scenarios critical for proxy-chain reliability:
// 1. Primary returns 429 (rate limit) -> fallback should handle
// 2. Primary returns 5xx (server error) -> fallback should handle
// 3. Both primary and fallback fail -> original error returned
// 4. Request body and headers are preserved during fallback
// 5. Proxy chain: router01 → router02 (primary) fails → router03 (fallback) succeeds,
//    router02 is called exactly once (not re-selected by the same-type retry loop)

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFallbackPath_PrimaryReturns429 tests that when primary credential returns 429,
// the request is retried with fallback credential and response is returned correctly.
func TestFallbackPath_PrimaryReturns429(t *testing.T) {
	// Track which server was called and how many times
	var primaryCalls, fallbackCalls int32

	// Create primary mock server (returns 429)
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&primaryCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate_limit_exceeded"})
	}))
	defer primaryServer.Close()

	// Create fallback mock server (returns 200 with success)
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fallbackCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "Hello from fallback"}}},
		})
	}))
	defer fallbackServer.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "primary",
				Type:       config.ProviderTypeProxy,
				APIKey:     "primary-key",
				BaseURL:    primaryServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "fallback",
				Type:       config.ProviderTypeProxy,
				APIKey:     "fallback-key",
				BaseURL:    fallbackServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	// Make request
	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Execute proxy request
	prx.ProxyRequest(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code, "Expected 200 OK from fallback")
	assert.Contains(t, w.Body.String(), "Hello from fallback", "Expected fallback response content")

	// Verify that primary was called first, then fallback
	assert.Equal(t, int32(1), primaryCalls, "Expected primary to be called once")
	assert.Equal(t, int32(1), fallbackCalls, "Expected fallback to be called once")
}

// TestFallbackPath_PrimaryReturns500 tests that when primary credential returns 500,
// the request is retried with fallback credential.
func TestFallbackPath_PrimaryReturns500(t *testing.T) {
	var primaryCalls, fallbackCalls int32

	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&primaryCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "internal_error"})
	}))
	defer primaryServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fallbackCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "chatcmpl-456",
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "Response from fallback"}},
			},
		})
	}))
	defer fallbackServer.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "primary",
				Type:       config.ProviderTypeProxy,
				APIKey:     "primary-key",
				BaseURL:    primaryServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "fallback",
				Type:       config.ProviderTypeProxy,
				APIKey:     "fallback-key",
				BaseURL:    fallbackServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Test"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "Expected 200 OK from fallback")
	assert.Contains(t, w.Body.String(), "Response from fallback")
	assert.Equal(t, int32(1), primaryCalls)
	assert.Equal(t, int32(1), fallbackCalls)
}

// TestFallbackPath_NoFallbackAvailable tests that when no fallback is available,
// the original error is returned to the client.
func TestFallbackPath_NoFallbackAvailable(t *testing.T) {
	var primaryCalls int32

	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&primaryCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate_limit_exceeded"})
	}))
	defer primaryServer.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "primary",
				Type:       config.ProviderTypeProxy,
				APIKey:     "primary-key",
				BaseURL:    primaryServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Test"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	// Original error should be returned
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, int32(1), primaryCalls, "Expected only primary to be called")
}

// TestFallbackPath_FallbackAlsoFails tests behavior when both primary and fallback fail.
func TestFallbackPath_FallbackAlsoFails(t *testing.T) {
	var primaryCalls, fallbackCalls int32

	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&primaryCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate_limit_exceeded"})
	}))
	defer primaryServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fallbackCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "fallback_error"})
	}))
	defer fallbackServer.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "primary",
				Type:       config.ProviderTypeProxy,
				APIKey:     "primary-key",
				BaseURL:    primaryServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "fallback",
				Type:       config.ProviderTypeProxy,
				APIKey:     "fallback-key",
				BaseURL:    fallbackServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Test"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	// Fallback error should be returned (it was attempted)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, int32(1), primaryCalls)
	assert.Equal(t, int32(1), fallbackCalls)
}

// TestFallbackPath_NonRetryableError tests that errors like "model not found" are NOT retried
// even if other conditions would trigger fallback.
func TestFallbackPath_NonRetryableError(t *testing.T) {
	var primaryCalls, fallbackCalls int32

	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&primaryCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "model does not exist"})
	}))
	defer primaryServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fallbackCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer fallbackServer.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "primary",
				Type:       config.ProviderTypeProxy,
				APIKey:     "primary-key",
				BaseURL:    primaryServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "fallback",
				Type:       config.ProviderTypeProxy,
				APIKey:     "fallback-key",
				BaseURL:    fallbackServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Test"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	// Original error returned (no retry attempted)
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, int32(1), primaryCalls)
	assert.Equal(t, int32(0), fallbackCalls, "Fallback should NOT be called for non-retryable error")
}

// TestFallbackPath_Streaming_NotSupported tests the current limitation that
// streaming requests don't support fallback retry when primary fails.
//
// NOTE: This test documents current behavior. Streaming fallback support
// would require architectural changes to buffer streaming data and retry.
func TestFallbackPath_Streaming_NotSupported(t *testing.T) {
	var primaryCalls, fallbackCalls int32

	// Primary returns 500 during streaming
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&primaryCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("data: {\"error\": \"server error\"}\n\n"))
	}))
	defer primaryServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&fallbackCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\": [...]}\n\n"))
		flusher.Flush()
	}))
	defer fallbackServer.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "primary",
				Type:       config.ProviderTypeProxy,
				APIKey:     "primary-key",
				BaseURL:    primaryServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "fallback",
				Type:       config.ProviderTypeProxy,
				APIKey:     "fallback-key",
				BaseURL:    fallbackServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Test"}], "stream": true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	// CURRENT BEHAVIOR: Streaming error is returned as-is, no fallback retry
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, int32(1), primaryCalls)
	assert.Equal(t, int32(0), fallbackCalls, "Fallback NOT called for streaming (current limitation)")

	// TODO: When streaming fallback support is implemented, this should be changed to:
	// - Expect fallback to be called
	// - Expect 200 response from fallback
}

// TestFallbackPath_RequestBodyIntegrity tests that request body is preserved
// when retrying with fallback proxy.
func TestFallbackPath_RequestBodyIntegrity(t *testing.T) {
	var primaryBody, fallbackBody []byte

	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		primaryBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate_limit"})
	}))
	defer primaryServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		fallbackBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-789",
			"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "OK"}}},
		})
	}))
	defer fallbackServer.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "primary",
				Type:       config.ProviderTypeProxy,
				APIKey:     "primary-key",
				BaseURL:    primaryServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "fallback",
				Type:       config.ProviderTypeProxy,
				APIKey:     "fallback-key",
				BaseURL:    fallbackServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	testBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}], "temperature": 0.7}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(testBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// Verify request body is identical in both calls
	assert.Equal(t, primaryBody, fallbackBody, "Request body should be identical for both primary and fallback")
	assert.Equal(t, testBody, string(primaryBody), "Request body should match original request")
}

// TestFallbackPath_HeadersPreserved tests that request headers are correctly forwarded to fallback.
func TestFallbackPath_HeadersPreserved(t *testing.T) {
	var fallbackHeaders http.Header

	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate_limit"})
	}))
	defer primaryServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHeaders = r.Header.Clone()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-999",
			"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "Success"}}},
		})
	}))
	defer fallbackServer.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "primary",
				Type:       config.ProviderTypeProxy,
				APIKey:     "primary-key",
				BaseURL:    primaryServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "fallback",
				Type:       config.ProviderTypeProxy,
				APIKey:     "fallback-key",
				BaseURL:    fallbackServer.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": []}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom-Header", "test-value")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// Custom header should be forwarded to fallback
	assert.Equal(t, "test-value", fallbackHeaders.Get("X-Custom-Header"))
	// Content-Type should be preserved
	assert.Equal(t, "application/json", fallbackHeaders.Get("Content-Type"))
}

// TestProxyChain_PrimaryFailsFallbackSucceeds_PrimaryCalledOnce tests the distributed
// proxy chain scenario:
//
//	router01 receives request
//	  └─► router02 (primary proxy) → returns 429
//	      └─► router01 detects retryable error
//	          └─► router03 (fallback proxy) → success
//
// Critical invariant: router02 must be called exactly ONCE.
// Before the fix (missing triedCreds[cred.Name]=true at loop start), router02 was
// re-selected on the first same-type retry because it was not yet in the exclude set,
// causing it to be called 2–3 times before the fallback was reached.
func TestProxyChain_PrimaryFailsFallbackSucceeds_PrimaryCalledOnce(t *testing.T) {
	var router02Calls, router03Calls int32

	// router02: primary proxy, always returns 429
	router02 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&router02Calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate_limit_exceeded"})
	}))
	defer router02.Close()

	// router03: fallback proxy, returns success
	router03 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&router03Calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "chatcmpl-router03",
			"object": "chat.completion",
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "Response from router03"}},
			},
		})
	}))
	defer router03.Close()

	// router01 configuration: router02 as primary, router03 as fallback
	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "router02",
				Type:       config.ProviderTypeProxy,
				APIKey:     "router02-key",
				BaseURL:    router02.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "router03",
				Type:       config.ProviderTypeProxy,
				APIKey:     "router03-key",
				BaseURL:    router03.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "Expected 200 OK from router03 fallback")
	assert.Contains(t, w.Body.String(), "router03", "Expected response from router03")

	// Primary (router02) must be called exactly once — not re-selected by same-type retry loop.
	// With the bug (missing triedCreds[cred.Name]=true), router02 was called 2+ times.
	assert.Equal(t, int32(1), router02Calls, "router02 must be called exactly once")
	assert.Equal(t, int32(1), router03Calls, "router03 (fallback) must be called exactly once")
}

// TestProxyChain_PrimaryFailsWith500_FallbackSucceeds tests the same chain
// with a 5xx server error from the primary proxy.
func TestProxyChain_PrimaryFailsWith500_FallbackSucceeds(t *testing.T) {
	var router02Calls, router03Calls int32

	router02 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&router02Calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "upstream_error"})
	}))
	defer router02.Close()

	router03 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&router03Calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "chatcmpl-router03-500",
			"object": "chat.completion",
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "Recovered via router03"}},
			},
		})
	}))
	defer router03.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "router02",
				Type:       config.ProviderTypeProxy,
				APIKey:     "router02-key",
				BaseURL:    router02.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
			config.CredentialConfig{
				Name:       "router03",
				Type:       config.ProviderTypeProxy,
				APIKey:     "router03-key",
				BaseURL:    router03.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: true,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "router03")
	assert.Equal(t, int32(1), router02Calls, "router02 must be called exactly once")
	assert.Equal(t, int32(1), router03Calls, "router03 (fallback) must be called exactly once")
}

// TestProxyChain_NoFallback_ErrorPropagated tests that when there is no fallback
// configured on the chain, the original error from the primary is returned to the client.
func TestProxyChain_NoFallback_ErrorPropagated(t *testing.T) {
	var router02Calls int32

	router02 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&router02Calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate_limit_exceeded"})
	}))
	defer router02.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			config.CredentialConfig{
				Name:       "router02",
				Type:       config.ProviderTypeProxy,
				APIKey:     "router02-key",
				BaseURL:    router02.URL,
				RPM:        100,
				TPM:        10000,
				IsFallback: false,
			},
		).
		Build()

	reqBody := `{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	prx.ProxyRequest(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code, "Original error must propagate when no fallback exists")
	assert.Equal(t, int32(1), router02Calls, "router02 must be called exactly once")
}
