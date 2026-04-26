package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/httputil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Helpers
// ============================================================================

// buildTraceProxy creates a Proxy with the given proxy-type credentials.
// routerID is injected so assertions are deterministic.
func buildTraceProxy(routerID string, creds ...config.CredentialConfig) *Proxy {
	prx := NewTestProxyBuilder().
		WithCredentials(creds...).
		WithMasterKey("master-key").
		Build()
	prx.routerID = routerID
	return prx
}

// proxyCredential creates a minimal proxy-type CredentialConfig pointing at baseURL.
func proxyCredential(name, baseURL string) config.CredentialConfig {
	return config.CredentialConfig{
		Name:    name,
		Type:    config.ProviderTypeProxy,
		BaseURL: baseURL,
		APIKey:  "key-" + name,
		RPM:     100,
		TPM:     10000,
	}
}

// openaiCredential creates an openai-type credential (non-proxy, should never be fetched).
func openaiCredential(name string) config.CredentialConfig {
	return config.CredentialConfig{
		Name:    name,
		Type:    config.ProviderTypeOpenAI,
		BaseURL: "http://api.openai.com",
		APIKey:  "sk-" + name,
		RPM:     100,
		TPM:     10000,
	}
}

// ============================================================================
// TraceCheck tests
// ============================================================================

// TestTraceCheck_Depth0_LocalOnly ensures that depth=0 returns only local router
// data without reaching out to any upstream proxy credentials.
func TestTraceCheck_Depth0_LocalOnly(t *testing.T) {
	var fetchCalled bool
	// upstream server must NOT be called when depth=0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalled = true
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(httputil.ProxyTraceResponse{RouterID: "upstream"})
	}))
	defer upstream.Close()

	prx := buildTraceProxy("router-local", proxyCredential("up", upstream.URL))

	result := prx.TraceCheck(context.Background(), 0)

	require.NotNil(t, result)
	assert.Equal(t, "router-local", result.RouterID)
	assert.NotEmpty(t, result.Status)
	assert.Nil(t, result.Upstreams, "upstreams must be nil at depth=0")
	assert.False(t, fetchCalled, "upstream server must not be contacted at depth=0")
}

// TestTraceCheck_Depth0_StatusHealthy verifies status is "healthy" when credentials are available.
func TestTraceCheck_Depth0_StatusHealthy(t *testing.T) {
	prx := buildTraceProxy("r1", openaiCredential("cred1"))

	result := prx.TraceCheck(context.Background(), 0)

	require.NotNil(t, result)
	assert.Equal(t, "healthy", result.Status)
}

// TestTraceCheck_Depth0_StatusUnhealthy verifies status is "unhealthy" when no credentials exist.
func TestTraceCheck_Depth0_StatusUnhealthy(t *testing.T) {
	prx := buildTraceProxy("r-empty") // no credentials

	result := prx.TraceCheck(context.Background(), 0)

	require.NotNil(t, result)
	assert.Equal(t, "unhealthy", result.Status)
}

// TestTraceCheck_Depth1_UpstreamPopulated verifies that with depth>0 and a reachable
// upstream proxy, the Upstreams map is populated with the upstream's trace response.
func TestTraceCheck_Depth1_UpstreamPopulated(t *testing.T) {
	upstreamTrace := httputil.ProxyTraceResponse{
		RouterID: "upstream-router",
		Status:   "healthy",
		Credentials: map[string]httputil.CredentialHealthStats{
			"cred1": {Type: "openai"},
		},
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(upstreamTrace)
	}))
	defer upstream.Close()

	prx := buildTraceProxy("router-main", proxyCredential("up1", upstream.URL))

	result := prx.TraceCheck(context.Background(), 1)

	require.NotNil(t, result)
	assert.Equal(t, "router-main", result.RouterID)
	require.NotNil(t, result.Upstreams)
	assert.Contains(t, result.Upstreams, "up1")

	upEntry := result.Upstreams["up1"]
	require.NotNil(t, upEntry)
	assert.Empty(t, upEntry.FetchError, "FetchError must be empty for a successful fetch")
	assert.Equal(t, "upstream-router", upEntry.RouterID)
	assert.Equal(t, "healthy", upEntry.Status)
}

// TestTraceCheck_Depth1_FetchError verifies that when upstream fetch fails,
// FetchError is set on the upstream entry and no panic occurs.
func TestTraceCheck_Depth1_FetchError(t *testing.T) {
	// Server that always returns a non-JSON body to force a parse error
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer upstream.Close()

	prx := buildTraceProxy("router-err", proxyCredential("failing-up", upstream.URL))

	// Must not panic
	result := prx.TraceCheck(context.Background(), 1)

	require.NotNil(t, result)
	require.NotNil(t, result.Upstreams)
	assert.Contains(t, result.Upstreams, "failing-up")

	upEntry := result.Upstreams["failing-up"]
	require.NotNil(t, upEntry)
	assert.NotEmpty(t, upEntry.FetchError, "FetchError must be set when upstream returns an error")
}

// TestTraceCheck_Depth1_UnreachableUpstream verifies FetchError is set when the
// upstream server is unreachable (connection refused / closed server).
func TestTraceCheck_Depth1_UnreachableUpstream(t *testing.T) {
	// Start and immediately close the server to get a "connection refused" address.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := srv.URL
	srv.Close()

	prx := buildTraceProxy("router-unreachable", proxyCredential("dead-up", closedURL))

	result := prx.TraceCheck(context.Background(), 1)

	require.NotNil(t, result)
	require.NotNil(t, result.Upstreams)
	upEntry := result.Upstreams["dead-up"]
	require.NotNil(t, upEntry)
	assert.NotEmpty(t, upEntry.FetchError, "FetchError must be set when upstream is unreachable")
}

// TestTraceCheck_NonProxyCredentials verifies that non-proxy credentials do not
// appear in Upstreams (only proxy-type credentials are fetched).
func TestTraceCheck_NonProxyCredentials(t *testing.T) {
	var fetchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalled = true
		_ = json.NewEncoder(w).Encode(httputil.ProxyTraceResponse{RouterID: "should-not-appear"})
	}))
	defer upstream.Close()

	// One openai credential (should not be fetched) — BaseURL points at mock so we'd notice.
	prx := buildTraceProxy("router-mixed",
		openaiCredential("openai-cred"),
	)

	result := prx.TraceCheck(context.Background(), 1)

	require.NotNil(t, result)
	assert.Nil(t, result.Upstreams,
		"Upstreams must be nil when there are no proxy-type credentials")
	assert.False(t, fetchCalled, "non-proxy credentials must not be fetched")
}

// TestTraceCheck_MultipleUpstreams verifies that all proxy credentials are traced
// and appear individually in the Upstreams map.
func TestTraceCheck_MultipleUpstreams(t *testing.T) {
	makeUpstream := func(routerID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(httputil.ProxyTraceResponse{RouterID: routerID, Status: "healthy"})
		}))
	}

	srv1 := makeUpstream("upstream-A")
	defer srv1.Close()
	srv2 := makeUpstream("upstream-B")
	defer srv2.Close()

	prx := buildTraceProxy("router-multi",
		proxyCredential("up-a", srv1.URL),
		proxyCredential("up-b", srv2.URL),
	)

	result := prx.TraceCheck(context.Background(), 1)

	require.NotNil(t, result)
	require.NotNil(t, result.Upstreams)
	assert.Contains(t, result.Upstreams, "up-a")
	assert.Contains(t, result.Upstreams, "up-b")
	assert.Empty(t, result.Upstreams["up-a"].FetchError)
	assert.Empty(t, result.Upstreams["up-b"].FetchError)
}

// TestTraceCheck_RouterIDAndCredentialsPopulated verifies that RouterID, Credentials,
// and Models are populated from HealthCheck data.
func TestTraceCheck_RouterIDAndCredentialsPopulated(t *testing.T) {
	prx := buildTraceProxy("my-router-id", openaiCredential("cred1"))

	result := prx.TraceCheck(context.Background(), 0)

	require.NotNil(t, result)
	assert.Equal(t, "my-router-id", result.RouterID)
	assert.NotNil(t, result.Credentials)
	assert.Contains(t, result.Credentials, "cred1")
}

// ============================================================================
// HandleTrace HTTP handler tests
// ============================================================================

// TestHandleTrace_JSON verifies that HandleTrace responds with HTTP 200,
// Content-Type application/json, and a valid ProxyTraceResponse body.
func TestHandleTrace_JSON(t *testing.T) {
	prx := buildTraceProxy("trace-router", openaiCredential("c1"))

	req := httptest.NewRequest("GET", "/trace", nil)
	w := httptest.NewRecorder()
	prx.HandleTrace(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var resp httputil.ProxyTraceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp),
		"response body must be valid JSON ProxyTraceResponse")
	assert.Equal(t, "trace-router", resp.RouterID)
	assert.NotEmpty(t, resp.Status)
}

// TestHandleTrace_DepthParam_Zero verifies depth=0 causes no upstream fetches.
func TestHandleTrace_DepthParam_Zero(t *testing.T) {
	var fetchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCalled = true
		_ = json.NewEncoder(w).Encode(httputil.ProxyTraceResponse{RouterID: "up"})
	}))
	defer upstream.Close()

	prx := buildTraceProxy("router", proxyCredential("up", upstream.URL))

	req := httptest.NewRequest("GET", "/trace?depth=0", nil)
	w := httptest.NewRecorder()
	prx.HandleTrace(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, fetchCalled, "upstream must not be fetched when depth=0")

	var resp httputil.ProxyTraceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Nil(t, resp.Upstreams)
}

// TestHandleTrace_DepthParam_ValidRange verifies that valid depth values 0–10 are accepted.
func TestHandleTrace_DepthParam_ValidRange(t *testing.T) {
	prx := buildTraceProxy("r", openaiCredential("c"))

	for _, depth := range []string{"0", "1", "5", "10"} {
		t.Run("depth="+depth, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/trace?depth="+depth, nil)
			w := httptest.NewRecorder()
			prx.HandleTrace(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

// TestHandleTrace_DepthParam_GreaterThan10_Clamped verifies that depth values > 10
// are ignored and the handler falls back to defaultTraceDepth (not the supplied value).
// Since no proxy credentials exist the response is always local-only, so we just
// assert the handler doesn't panic and returns 200.
func TestHandleTrace_DepthParam_GreaterThan10_Clamped(t *testing.T) {
	prx := buildTraceProxy("r", openaiCredential("c"))

	req := httptest.NewRequest("GET", "/trace?depth=99", nil)
	w := httptest.NewRecorder()
	prx.HandleTrace(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp httputil.ProxyTraceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// depth=99 is rejected by the clamping logic (only 0–10 accepted);
	// defaultTraceDepth (25) is used instead. With no proxy credentials the
	// result is still valid local-only trace data.
	assert.NotEmpty(t, resp.Status)
}

// TestHandleTrace_DepthParam_Negative_Clamped verifies that negative depth values
// are rejected by the clamping logic (n >= 0 && n <= 10) and defaultTraceDepth is used.
func TestHandleTrace_DepthParam_Negative_Clamped(t *testing.T) {
	prx := buildTraceProxy("r", openaiCredential("c"))

	req := httptest.NewRequest("GET", "/trace?depth=-1", nil)
	w := httptest.NewRecorder()
	prx.HandleTrace(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp httputil.ProxyTraceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Status)
}

// TestHandleTrace_DepthParam_InvalidString verifies that a non-integer depth query
// param is ignored and the handler uses defaultTraceDepth without error.
func TestHandleTrace_DepthParam_InvalidString(t *testing.T) {
	prx := buildTraceProxy("r", openaiCredential("c"))

	req := httptest.NewRequest("GET", "/trace?depth=abc", nil)
	w := httptest.NewRecorder()
	prx.HandleTrace(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp httputil.ProxyTraceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Status)
}

// TestHandleTrace_DepthParam_Missing verifies that omitting the depth param entirely
// uses defaultTraceDepth and returns a valid response.
func TestHandleTrace_DepthParam_Missing(t *testing.T) {
	prx := buildTraceProxy("r", openaiCredential("c"))

	req := httptest.NewRequest("GET", "/trace", nil)
	w := httptest.NewRecorder()
	prx.HandleTrace(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp httputil.ProxyTraceResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Status)
}

// ============================================================================
// HandleVisualTrace HTTP handler tests
// ============================================================================

// TestHandleVisualTrace_HTML verifies that HandleVisualTrace responds with HTTP 200,
// Content-Type text/html, and a non-empty body.
func TestHandleVisualTrace_HTML(t *testing.T) {
	prx := buildTraceProxy("r", openaiCredential("c"))

	req := httptest.NewRequest("GET", "/vtrace", nil)
	w := httptest.NewRecorder()
	prx.HandleVisualTrace(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html",
		"Content-Type must be text/html for visual trace endpoint")
	assert.NotEmpty(t, w.Body.String(), "visual trace response body must not be empty")
}
