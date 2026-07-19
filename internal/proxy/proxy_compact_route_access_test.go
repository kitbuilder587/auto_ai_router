package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	dbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompactAuthorizesOriginalRouteBeforeBodyAndInternalChatTransform(t *testing.T) {
	var providerCalls atomic.Int64
	provider := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-compact",
			"object":"chat.completion",
			"created":1784030400,
			"model":"backend-chat",
			"choices":[{"index":0,"message":{"role":"assistant","content":"compact summary"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
		}`))
	}))
	defer provider.Close()

	db := &clientAuthTestDB{tokens: map[string]*dbmodels.TokenInfo{
		"chat-only": {
			Token:         "chat-only-hash",
			AllowedRoutes: []string{"/v1/chat/completions"},
		},
		"compact-exact": {
			Token:         "compact-exact-hash",
			AllowedRoutes: []string{"/v1/responses/compact"},
		},
		"responses-prefix": {
			Token:         "responses-prefix-hash",
			AllowedRoutes: []string{"/v1/responses"},
		},
		"llm-routes": {
			Token:         "llm-routes-hash",
			AllowedRoutes: []string{"llm_api_routes"},
		},
		"management": {
			Token:         "management-hash",
			AllowedRoutes: []string{"management_routes"},
		},
	}}
	prx := newClientAuthTestProxy(t, db, provider.URL, config.ProviderTypeOpenAI, "provider-key")

	tests := []struct {
		name             string
		token            string
		body             string
		wantStatus       int
		wantProviderCall bool
	}{
		{name: "chat-only cannot inherit implementation route", token: "chat-only", body: `{"model":"public/chat","input":"hello"}`, wantStatus: http.StatusForbidden},
		{name: "exact compact route", token: "compact-exact", body: `{"model":"public/chat","input":"hello"}`, wantStatus: http.StatusOK, wantProviderCall: true},
		{name: "responses prefix", token: "responses-prefix", body: `{"model":"public/chat","input":"hello"}`, wantStatus: http.StatusOK, wantProviderCall: true},
		{name: "llm route group", token: "llm-routes", body: `{"model":"public/chat","input":"hello"}`, wantStatus: http.StatusOK, wantProviderCall: true},
		{name: "management denied", token: "management", body: `{"model":"public/chat","input":"hello"}`, wantStatus: http.StatusForbidden},
		{name: "management denied before malformed body", token: "management", body: `{`, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := providerCalls.Load()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+tt.token)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			prx.HandleCompactResponse(recorder, req)

			require.Equal(t, tt.wantStatus, recorder.Code, recorder.Body.String())
			if tt.wantProviderCall {
				assert.Equal(t, before+1, providerCalls.Load())
				assert.Contains(t, recorder.Body.String(), `"object":"response.compaction"`)
			} else {
				assert.Equal(t, before, providerCalls.Load())
			}
		})
	}
}
