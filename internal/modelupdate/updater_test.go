package modelupdate

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/balancer"
	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/fail2ban"
	"github.com/mixaill76/auto_ai_router/internal/httputil"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/ratelimit"
	"github.com/stretchr/testify/assert"
)

func TestSplitCredentialModel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "standard credential:model",
			input:    "cred:model",
			expected: []string{"cred", "model"},
		},
		{
			name:     "no colon returns single element",
			input:    "no-colon",
			expected: []string{"no-colon"},
		},
		{
			name:     "multiple colons splits on first only",
			input:    "a:b:c",
			expected: []string{"a", "b:c"},
		},
		{
			name:     "empty string",
			input:    "",
			expected: []string{""},
		},
		{
			name:     "colon at start",
			input:    ":model-name",
			expected: []string{"", "model-name"},
		},
		{
			name:     "colon at end",
			input:    "credential:",
			expected: []string{"credential", ""},
		},
		{
			name:     "only colon",
			input:    ":",
			expected: []string{"", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SplitCredentialModel(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUpdateAllProxyCredentials_DoesNotCreateDefaultModelLimitsFromDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&httputil.ProxyHealthResponse{
				Credentials: map[string]httputil.CredentialHealthStats{
					"remote_cred_1": {Weight: 3},
				},
				Models: map[string]httputil.ModelHealthStats{
					"zero_only": {
						Credential: "remote_cred_1",
						Model:      "zero-only-model",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rl := ratelimit.New()
	creds := []config.CredentialConfig{
		{
			Name:    "proxy",
			Type:    config.ProviderTypeProxy,
			BaseURL: server.URL,
			APIKey:  "unused",
			RPM:     -1,
		},
	}
	bal := balancer.New(creds, fail2ban.New(3, time.Minute, nil), rl)
	manager := models.New(slog.New(slog.NewTextHandler(io.Discard, nil)), 100, nil)
	var updateMutex sync.Mutex

	UpdateAllProxyCredentials(context.Background(), bal, rl, slog.New(slog.NewTextHandler(io.Discard, nil)), manager, &updateMutex)

	assert.True(t, manager.HasModel("proxy", "zero-only-model"))
	assert.Equal(t, 3, manager.GetModelWeightForCredential("zero-only-model", "proxy"))
	assert.Empty(t, rl.GetAllModelPairs(), "model discovery alone must not create default RPM/TPM limits")
}
