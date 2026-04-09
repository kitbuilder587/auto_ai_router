package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatCompletionOKBody returns a minimal valid JSON chat completion response.
func chatCompletionOKBody(id, content string) string {
	resp := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion",
		"created": 1234567890,
		"model":   "gpt-4",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// makeChatRequest builds a standard POST /v1/chat/completions request with the given session ID
// embedded in the "user" field (which is one of the fields extracted by extractMetadataFromBody).
func makeChatRequest(sessionID string) *http.Request {
	var body string
	if sessionID != "" {
		body = `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"user":"` + sessionID + `"}`
	} else {
		body = `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer master-key")
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestProxyRequest_StickyDisabled_SessionStoreIsNil verifies that when
// SessionStickyEnabled is false, the proxy creates no session store and
// requests still succeed via normal round-robin credential selection.
func TestProxyRequest_StickyDisabled_SessionStoreIsNil(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody("id-1", "response")))
	}))
	defer upstream.Close()

	prx := NewTestProxyBuilder().
		WithSingleCredential("cred1", config.ProviderTypeProxy, upstream.URL, "key1").
		Build() // SessionStickyEnabled defaults to false

	assert.Nil(t, prx.sessionStore, "sessionStore must be nil when SessionStickyEnabled=false")

	req := makeChatRequest("sess-disabled-1")
	w := httptest.NewRecorder()
	prx.ProxyRequest(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestProxyRequest_StickyEnabled_UsesBindingIfAvailable verifies that when a
// session binding exists, the proxy routes to the bound credential rather than
// following round-robin order.
func TestProxyRequest_StickyEnabled_UsesBindingIfAvailable(t *testing.T) {
	// server1 answers with "from-cred1"; server2 with "from-cred2".
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody("id-cred1", "from-cred1")))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody("id-cred2", "from-cred2")))
	}))
	defer server2.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			testProxyCredential("cred1", server1.URL, false),
			testProxyCredential("cred2", server2.URL, false),
		).
		WithSessionSticky(5 * time.Minute).
		Build()

	require.NotNil(t, prx.sessionStore, "sessionStore must be created when SessionStickyEnabled=true")

	// Pre-seed a binding: session "sess-sticky-1" for model "gpt-4" → "cred2"
	prx.sessionStore.Set("sess-sticky-1", "gpt-4", "cred2")

	req := makeChatRequest("sess-sticky-1")
	w := httptest.NewRecorder()
	prx.ProxyRequest(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	// The response must come from cred2, not cred1.
	assert.Contains(t, w.Body.String(), "from-cred2",
		"session-sticky routing should route to the bound credential (cred2)")
	assert.NotContains(t, w.Body.String(), "from-cred1")
}

// TestProxyRequest_StickyBinding_ClearedOnFailure verifies that when the upstream
// returns an error and the request is not completed, the session binding is deleted
// by the defer in ProxyRequest (clearSessionBinding when RequestCompleted == false).
func TestProxyRequest_StickyBinding_ClearedOnFailure(t *testing.T) {
	// Upstream always returns 500 — request will not be marked as completed.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	}))
	defer upstream.Close()

	prx := NewTestProxyBuilder().
		WithSingleCredential("cred1", config.ProviderTypeProxy, upstream.URL, "key1").
		WithSessionSticky(5 * time.Minute).
		Build()

	require.NotNil(t, prx.sessionStore)

	// Pre-seed a binding before the request.
	prx.sessionStore.Set("sess-fail-1", "gpt-4", "cred1")
	require.Equal(t, 1, prx.sessionStore.Len(), "binding must exist before request")

	req := makeChatRequest("sess-fail-1")
	w := httptest.NewRecorder()
	prx.ProxyRequest(w, req)

	// The upstream returned 500; the proxy may return 500 or a gateway error.
	// What matters is that the binding was cleared.
	assert.Equal(t, 0, prx.sessionStore.Len(),
		"session binding must be cleared after a failed (non-completed) request")
}

// TestProxyRequest_StickyBinding_SetOnSuccess verifies that after a successful
// request the proxy persists a session binding so subsequent requests are routed
// to the same credential.
func TestProxyRequest_StickyBinding_SetOnSuccess(t *testing.T) {
	var callCount int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody("id-ok", "ok-response")))
	}))
	defer upstream.Close()

	prx := NewTestProxyBuilder().
		WithSingleCredential("cred1", config.ProviderTypeProxy, upstream.URL, "key1").
		WithSessionSticky(5 * time.Minute).
		Build()

	require.NotNil(t, prx.sessionStore)

	const sessionID = "sess-success-1"

	// No binding exists before the first request.
	_, exists := prx.sessionStore.Get(sessionID, "gpt-4")
	assert.False(t, exists, "no binding should exist before first request")

	req := makeChatRequest(sessionID)
	w := httptest.NewRecorder()
	prx.ProxyRequest(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// After a successful request the binding must be set.
	credName, exists := prx.sessionStore.Get(sessionID, "gpt-4")
	assert.True(t, exists, "binding must be created after a successful request")
	assert.Equal(t, "cred1", credName, "binding must point to the credential that served the request")
}

// TestProxyRequest_StickyCredentialUnavailable_FallsBackToNormalSelection verifies
// that when the session-bound credential is not in the balancer (e.g. removed or
// renamed), the proxy transparently falls back to normal round-robin selection and
// still serves the request successfully.
func TestProxyRequest_StickyCredentialUnavailable_FallsBackToNormalSelection(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody("id-cred1", "from-cred1")))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody("id-cred2", "from-cred2")))
	}))
	defer server2.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			testProxyCredential("cred1", server1.URL, false),
			testProxyCredential("cred2", server2.URL, false),
		).
		WithSessionSticky(5 * time.Minute).
		Build()

	require.NotNil(t, prx.sessionStore)

	// Bind the session to a credential that does not exist in the balancer.
	prx.sessionStore.Set("sess-missing-1", "gpt-4", "nonexistent-cred")

	req := makeChatRequest("sess-missing-1")
	w := httptest.NewRecorder()
	prx.ProxyRequest(w, req)

	// Request must still succeed via one of the real credentials.
	require.Equal(t, http.StatusOK, w.Code,
		"proxy must fall back to a real credential when the bound one is unavailable")

	body := w.Body.String()
	assert.True(t,
		strings.Contains(body, "from-cred1") || strings.Contains(body, "from-cred2"),
		"response must come from one of the available credentials")
}

// TestProxyRequest_StickyEnabled_BindingUpdatedAfterRoundRobin verifies that when
// a session has no pre-existing binding, round-robin selects a credential and the
// proxy then writes a binding for future requests.
func TestProxyRequest_StickyEnabled_BindingUpdatedAfterRoundRobin(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody("id-rr-1", "rr-response")))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionOKBody("id-rr-2", "rr-response")))
	}))
	defer server2.Close()

	prx := NewTestProxyBuilder().
		WithCredentials(
			testProxyCredential("cred1", server1.URL, false),
			testProxyCredential("cred2", server2.URL, false),
		).
		WithSessionSticky(5 * time.Minute).
		Build()

	require.NotNil(t, prx.sessionStore)

	const sessionID = "sess-rr-1"

	req := makeChatRequest(sessionID)
	w := httptest.NewRecorder()
	prx.ProxyRequest(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// After the first successful request a binding must exist.
	credName, exists := prx.sessionStore.Get(sessionID, "gpt-4")
	require.True(t, exists, "binding must be set after a successful round-robin request")
	assert.True(t, credName == "cred1" || credName == "cred2",
		"binding must point to one of the real credentials, got: %q", credName)
}
