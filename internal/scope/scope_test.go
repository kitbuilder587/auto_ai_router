package scope

import "testing"

func TestContextAllows_DefaultCredentialVisibleToEveryone(t *testing.T) {
	ctx := PublicContext()

	if !ctx.Allows(nil, nil) {
		t.Fatal("unscoped credential must be visible to public context")
	}
}

func TestContextAllows_AllowAndDeny(t *testing.T) {
	ctx := NewContext([]string{"Team-A"}, nil)

	if !ctx.Allows([]string{"team-a"}, nil) {
		t.Fatal("matching allow scope must pass")
	}
	if ctx.Allows([]string{"team-b"}, nil) {
		t.Fatal("non-matching allow scope must not pass")
	}
	if ctx.Allows([]string{"team-a"}, []string{"team-a"}) {
		t.Fatal("credential deny scope must override allow")
	}
}

func TestContextAllows_RequestDeniedScopesOverrideCredentialAllow(t *testing.T) {
	ctx := NewContext([]string{"team-a"}, []string{"premium"})

	if ctx.Allows([]string{"team-a", "premium"}, nil) {
		t.Fatal("request denied scope must override credential allow")
	}
}

func TestContextAllows_AdminBypassesScopes(t *testing.T) {
	ctx := AdminContext()

	if !ctx.Allows([]string{"private"}, []string{"private"}) {
		t.Fatal("admin context must bypass scope rules")
	}
}
