package models

import (
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/httputil"
	"github.com/stretchr/testify/assert"
)

func TestAggregateModelScopesFromHealth(t *testing.T) {
	health := &httputil.ProxyHealthResponse{
		Credentials: map[string]httputil.CredentialHealthStats{
			"team-a": {Scopes: []string{"team-a"}},
			"team-b": {Scopes: []string{"team-b"}},
		},
		Models: map[string]httputil.ModelHealthStats{
			"a": {Credential: "team-a", Model: "gpt-4"},
			"b": {Credential: "team-b", Model: "claude-3"},
		},
	}

	scopes := AggregateModelScopesFromHealth(health, false)

	assert.Equal(t, []string{"team-a"}, scopes["gpt-4"].Scopes)
	assert.Equal(t, []string{"team-b"}, scopes["claude-3"].Scopes)
}

func TestAggregateProviderScopes_UnscopedDenyFilledByScopedCredential(t *testing.T) {
	health := &httputil.ProxyHealthResponse{
		Credentials: map[string]httputil.CredentialHealthStats{
			"shared": {DeniedScopes: []string{"team-a"}},
			"team-a": {Scopes: []string{"team-a"}},
		},
	}

	scopes := AggregateProviderScopesFromHealth(health, false)

	assert.Empty(t, scopes.Scopes)
	assert.Empty(t, scopes.DeniedScopes)
}
