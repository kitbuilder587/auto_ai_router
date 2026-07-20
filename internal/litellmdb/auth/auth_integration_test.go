//go:build integration

package auth

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/connection"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newIntegrationAuthenticator provisions an isolated schema with the auth
// fixture and returns an Authenticator bound to it. Each caller gets its own
// cache so subtests cannot poison each other through cached tokens.
func newIntegrationAuthenticator(t *testing.T, ctx context.Context) (*Authenticator, *pgx.Conn) {
	t.Helper()

	baseDSN := os.Getenv("SPEND_TEST_DATABASE_URL")
	if baseDSN == "" {
		t.Fatal("SPEND_TEST_DATABASE_URL is required for integration tests")
	}

	admin, err := pgx.Connect(ctx, baseDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	schemaName := "air_auth_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	})
	_, err = admin.Exec(ctx, "SET search_path TO "+quotedSchema)
	require.NoError(t, err)

	schemaSQL, err := os.ReadFile("testdata/litellm_auth_schema.sql")
	require.NoError(t, err)
	_, err = admin.Exec(ctx, string(schemaSQL))
	require.NoError(t, err)

	parsed, err := url.Parse(baseDSN)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()

	cfg := &models.Config{
		DatabaseURL: parsed.String(),
		Logger:      slog.Default(),
	}
	pool, err := connection.NewConnectionPool(cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	cache, err := NewCache(100, time.Minute)
	require.NoError(t, err)

	return NewAuthenticator(pool, cache, slog.Default()), admin
}

func TestAuthenticator_Integration_DeprecatedKeyGracePeriod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth, admin := newIntegrationAuthenticator(t, ctx)

	_, err := admin.Exec(ctx,
		`INSERT INTO "LiteLLM_VerificationToken" (token, models) VALUES ('active-hash', '{gpt-4}')`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx,
		`INSERT INTO "LiteLLM_DeprecatedVerificationToken" (token, active_token_id, revoke_at)
		 VALUES ('old-hash', 'active-hash', now() + interval '1 hour'),
		        ('expired-hash', 'active-hash', now() - interval '1 minute')`)
	require.NoError(t, err)

	info, err := auth.ValidateToken(ctx, "old-hash")
	require.NoError(t, err)
	// Spend attribution must follow the active token, exactly like LiteLLM's
	// combined_view re-fetch by active_token_id.
	assert.Equal(t, "active-hash", info.Token)
	assert.Equal(t, []string{"gpt-4"}, info.Models)

	_, err = auth.ValidateToken(ctx, "expired-hash")
	assert.ErrorIs(t, err, models.ErrTokenNotFound)

	_, err = auth.ValidateToken(ctx, "never-existed-hash")
	assert.ErrorIs(t, err, models.ErrTokenNotFound)
}

func TestAuthenticator_Integration_AccessGroupModelExpansion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth, admin := newIntegrationAuthenticator(t, ctx)

	_, err := admin.Exec(ctx,
		`INSERT INTO "LiteLLM_AccessGroupTable" (access_group_id, access_model_names, assigned_team_ids, assigned_key_ids)
		 VALUES ('ag-plain',  '{claude-3}', '{}',        '{}'),
		        ('ag-owned',  '{o3}',      '{team-ag}', '{}'),
		        ('ag-teamed', '{gemini-pro}', '{}',     '{}')`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx,
		`INSERT INTO "LiteLLM_TeamTable" (team_id, models, access_group_ids)
		 VALUES ('team-ag',  '{gpt-4}', '{}'),
		        ('team-grp', '{gpt-4}', '{ag-teamed}')`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx,
		`INSERT INTO "LiteLLM_VerificationToken" (token, team_id, models, access_group_ids)
		 VALUES ('tok-key-only', NULL,       '{gpt-4}', '{ag-plain}'),
		        ('tok-team',     'team-ag',  '{gpt-4}', '{ag-plain}'),
		        ('tok-owned',    'team-ag',  '{gpt-4}', '{ag-owned}'),
		        ('tok-teamgrp',  'team-grp', '{gpt-4,gemini-pro}', '{}')`)
	require.NoError(t, err)

	// can_key_call_model fallback: key group grants beyond the native list.
	_, err = auth.ValidateTokenForModel(ctx, "tok-key-only", "claude-3")
	assert.NoError(t, err)
	_, err = auth.ValidateTokenForModel(ctx, "tok-key-only", "gemini-pro")
	assert.ErrorIs(t, err, models.ErrModelNotAllowed)

	// An unowned key group cannot override the team's model restriction.
	_, err = auth.ValidateTokenForModel(ctx, "tok-team", "claude-3")
	assert.ErrorIs(t, err, models.ErrModelNotAllowed)

	// _key_access_group_grants_model: assigned_team_ids authorizes the
	// override, so the grant passes both key and team scopes.
	_, err = auth.ValidateTokenForModel(ctx, "tok-owned", "o3")
	assert.NoError(t, err)

	// can_team_access_model fallback: the team's own access group widens the
	// team scope for a model already present on the key.
	_, err = auth.ValidateTokenForModel(ctx, "tok-teamgrp", "gemini-pro")
	assert.NoError(t, err)
	_, err = auth.ValidateTokenForModel(ctx, "tok-teamgrp", "o3")
	assert.ErrorIs(t, err, models.ErrModelNotAllowed)
}
