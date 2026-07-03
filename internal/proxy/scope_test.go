package proxy

import (
	"testing"

	dbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
)

func TestScopeContextFromTokenInfo_FallsBackToKeyName(t *testing.T) {
	ctx := scopeContextFromTokenInfo(&dbmodels.TokenInfo{KeyName: "Team-A"})

	if !ctx.Allows([]string{"team-a"}, nil) {
		t.Fatal("key_name must become the request scope")
	}
	if ctx.Allows([]string{"team-b"}, nil) {
		t.Fatal("different credential scope must be hidden")
	}
}

func TestScopeContextFromTokenInfo_MetadataScopesAndDeniedScopes(t *testing.T) {
	ctx := scopeContextFromTokenInfo(&dbmodels.TokenInfo{
		KeyName: "ignored",
		Metadata: map[string]interface{}{
			"air_scopes":        []interface{}{"team-b"},
			"air_denied_scopes": "premium,blocked",
		},
	})

	if !ctx.Allows([]string{"team-b"}, nil) {
		t.Fatal("metadata air_scopes must be used")
	}
	if ctx.Allows([]string{"ignored"}, nil) {
		t.Fatal("metadata scopes must override key_name fallback")
	}
	if ctx.Allows([]string{"team-b", "premium"}, nil) {
		t.Fatal("metadata denied scopes must override allow")
	}
}
