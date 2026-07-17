//go:build integration

package shadowspend

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const integrationKeyHash = "cc557cce629a1cb98664b98a3d5f5600a90a91c5955c4fdddfa4d13c94bfdcd6"

func TestShadowSink_PostgreSQLLiteLLMContract(t *testing.T) {
	baseDSN := os.Getenv("SHADOW_TEST_DATABASE_URL")
	if baseDSN == "" {
		t.Fatal("SHADOW_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, baseDSN)
	require.NoError(t, err)
	defer func() { require.NoError(t, admin.Close(context.Background())) }()

	var databaseName string
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_database()").Scan(&databaseName))
	schemaName := "air_shadow_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	}()
	_, err = admin.Exec(ctx, "SET search_path TO "+quotedSchema)
	require.NoError(t, err)
	installIntegrationSchema(t, ctx, admin)
	seedCounterRows(t, ctx, admin)

	guardCfg := integrationSpendConfig(baseDSN, databaseName)
	guardCfg.ExpectedDatabaseName = databaseName + "_wrong"
	guardSink, guardErr := New(ctx, guardCfg, slog.Default())
	assert.Nil(t, guardSink)
	assert.True(t, errors.Is(guardErr, ErrUnexpectedDatabase))

	dsn := withSearchPath(t, baseDSN, schemaName)
	sink, err := New(ctx, integrationSpendConfig(dsn, databaseName), slog.Default())
	require.NoError(t, err)
	require.True(t, sink.IsEnabled())

	identity := verifiedIntegrationIdentity(t)
	entries := integrationEntries(identity)
	for _, entry := range entries {
		require.NoError(t, sink.LogSpend(entry))
	}
	// A writer retry of the same billing event must be a no-op for raw, counters,
	// and daily aggregates.
	require.NoError(t, sink.LogSpend(entries[0]))

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	require.NoError(t, sink.Shutdown(shutdownCtx))
	shutdownCancel()

	assertRawRowsAndPrivacy(t, ctx, admin, len(entries))
	assertCounterIdempotency(t, ctx, admin, float64(len(entries))*0.001)
	assertDailyDimensions(t, ctx, admin, len(entries))
}

func integrationSpendConfig(dsn, databaseName string) config.SpendLogConfig {
	return config.SpendLogConfig{
		Mode:                 config.SpendLogModeShadow,
		DatabaseURL:          dsn,
		ExpectedDatabaseName: databaseName,
		MaxConns:             4,
		MinConns:             1,
		HealthCheckInterval:  100 * time.Millisecond,
		ConnectTimeout:       5 * time.Second,
		LogQueueSize:         100,
		LogBatchSize:         20,
		LogFlushInterval:     25 * time.Millisecond,
	}
}

func installIntegrationSchema(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	schemaSQL, err := os.ReadFile("testdata/litellm_shadow_schema.sql")
	require.NoError(t, err)
	for _, statement := range strings.Split(string(schemaSQL), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" || strings.HasPrefix(statement, "--") && !strings.Contains(statement, "\n") {
			continue
		}
		// Preserve statements that start with comments by removing comment-only lines.
		lines := strings.Split(statement, "\n")
		filtered := lines[:0]
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			filtered = append(filtered, line)
		}
		statement = strings.TrimSpace(strings.Join(filtered, "\n"))
		if statement == "" {
			continue
		}
		_, err := conn.Exec(ctx, statement)
		require.NoError(t, err, statement)
	}
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func seedCounterRows(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	statements := []string{
		`INSERT INTO "LiteLLM_VerificationToken" (token) VALUES ($1)`,
		`INSERT INTO "LiteLLM_UserTable" (user_id) VALUES ('user-it')`,
		`INSERT INTO "LiteLLM_TeamTable" (team_id) VALUES ('team-it')`,
		`INSERT INTO "LiteLLM_OrganizationTable" (organization_id) VALUES ('org-it')`,
		`INSERT INTO "LiteLLM_ProjectTable" (project_id) VALUES ('project-it')`,
		`INSERT INTO "LiteLLM_TeamMembership" (team_id, user_id) VALUES ('team-it', 'user-it')`,
		`INSERT INTO "LiteLLM_OrganizationMembership" (organization_id, user_id) VALUES ('org-it', 'user-it')`,
		`INSERT INTO "LiteLLM_EndUserTable" (user_id) VALUES ('end-user-it')`,
		`INSERT INTO "LiteLLM_TagTable" (tag_name) VALUES ('tag-it')`,
		`INSERT INTO "LiteLLM_AgentsTable" (agent_id, agent_name) VALUES ('agent-it', 'agent-it-name')`,
	}
	for index, statement := range statements {
		var err error
		if index == 0 {
			_, err = conn.Exec(ctx, statement, integrationKeyHash)
		} else {
			_, err = conn.Exec(ctx, statement)
		}
		require.NoError(t, err)
	}
}

func verifiedIntegrationIdentity(t *testing.T) shadowcontext.Identity {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	verifier, err := shadowcontext.NewVerifier(config.ShadowAuthContextConfig{
		Issuer:          "litellm-it",
		Audience:        "air-it",
		PublicKeys:      map[string]string{"it": base64.RawURLEncoding.EncodeToString(publicKey)},
		ClockSkew:       10 * time.Second,
		ReplayCacheSize: 100,
	})
	require.NoError(t, err)
	now := time.Now()
	claims := shadowcontext.Claims{
		Issuer: "litellm-it", Audience: shadowcontext.Audience{"air-it"},
		IssuedAt: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(time.Minute).Unix(), ID: uuid.NewString(),
		APIKeyHash: integrationKeyHash, UserID: "user-it", TeamID: "team-it", OrganizationID: "org-it",
		ProjectID: "project-it", AgentID: "agent-it", PublicModel: "public-it", DeploymentID: "deployment-it",
		EndUser: "end-user-it", Tags: []string{"tag-it"}, OriginalCallType: "acompletion", CallID: "call-it",
	}
	protected, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "it"})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	encodedProtected := base64.RawURLEncoding.EncodeToString(protected)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := encodedProtected + "." + encodedPayload
	compact := signingInput + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(signingInput)))
	result := verifier.Verify(compact)
	require.NoError(t, result.Err)
	require.Equal(t, shadowcontext.StateValid, result.State)
	return result.Identity
}

func integrationEntries(identity shadowcontext.Identity) []*models.SpendLogEntry {
	now := time.Now().UTC().Truncate(time.Millisecond)
	routes := []struct {
		requestID string
		callType  string
		status    string
	}{
		{"chatcmpl-it", "acompletion", "success"},
		{"cmpl-it", "atext_completion", "success"},
		{"event-embedding-it", "aembedding", "success"},
		{"resp-it", "aresponses", "failure"},
		{"event-image-generation-it", "aimage_generation", "success"},
		{"event-image-edit-it", "aimage_edit", "success"},
	}
	entries := make([]*models.SpendLogEntry, 0, len(routes))
	for index, route := range routes {
		metadata := fmt.Sprintf(`{"litellm_call_id":"call-it-%d","status":%q,"usage_object":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2,"cache_creation_tokens":1}},"cost_breakdown":{"total_cost":0.001},"spend_logs_metadata":{"comparison_eligible":true,"shadow_context_state":"valid","usage_source":"provider","original_call_type":%q}}`, index, route.status, route.callType)
		rawCallType := route.callType
		if route.status == "failure" {
			rawCallType = ""
		}
		entries = append(entries, &models.SpendLogEntry{
			RequestID: route.requestID, CallType: rawCallType, APIKey: identity.APIKeyHash,
			Spend: 0.001, TotalTokens: 15, PromptTokens: 10, CompletionTokens: 5,
			StartTime: now.Add(time.Duration(index) * time.Millisecond), EndTime: now.Add(time.Duration(index+1) * time.Millisecond), RequestDurationMS: 1,
			Model: "backend-it", ModelID: identity.DeploymentID, ModelGroup: identity.PublicModel,
			CustomLLMProvider: "openai", APIBase: "http://air-ru01/v1", UserID: identity.UserID,
			Metadata: metadata, CacheHit: "False", CacheKey: "Cache OFF", RequestTags: `["tag-it"]`,
			TeamID: identity.TeamID, OrganizationID: identity.OrganizationID, ProjectID: identity.ProjectID, EndUser: identity.EndUser,
			RequesterIP: "192.0.2.10", SessionID: "session-it", Status: route.status, AgentID: identity.AgentID,
			ComparisonEligible: true,
		})
	}
	return entries
}

func assertRawRowsAndPrivacy(t *testing.T, ctx context.Context, conn *pgx.Conn, expected int) {
	t.Helper()
	var count, privateNotEmpty, mcpNonNull, unexpectedTeam int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM "LiteLLM_SpendLogs"`).Scan(&count))
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM "LiteLLM_SpendLogs" WHERE messages IS DISTINCT FROM '{}'::jsonb OR response IS DISTINCT FROM '{}'::jsonb OR proxy_server_request IS DISTINCT FROM '{}'::jsonb`).Scan(&privateNotEmpty))
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM "LiteLLM_SpendLogs" WHERE mcp_namespaced_tool_name IS NOT NULL`).Scan(&mcpNonNull))
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM "LiteLLM_SpendLogs" WHERE team_id = 'image-credential' OR team_id = 'provider-credential'`).Scan(&unexpectedTeam))
	assert.Equal(t, expected, count)
	assert.Zero(t, privateNotEmpty)
	assert.Zero(t, mcpNonNull)
	assert.Zero(t, unexpectedTeam)
}

func assertCounterIdempotency(t *testing.T, ctx context.Context, conn *pgx.Conn, expected float64) {
	t.Helper()
	queries := []struct {
		query string
		args  []any
	}{
		{`SELECT spend FROM "LiteLLM_VerificationToken" WHERE token=$1`, []any{integrationKeyHash}},
		{`SELECT spend FROM "LiteLLM_UserTable" WHERE user_id='user-it'`, nil},
		{`SELECT spend FROM "LiteLLM_TeamTable" WHERE team_id='team-it'`, nil},
		{`SELECT spend FROM "LiteLLM_OrganizationTable" WHERE organization_id='org-it'`, nil},
		{`SELECT spend FROM "LiteLLM_ProjectTable" WHERE project_id='project-it'`, nil},
		{`SELECT spend FROM "LiteLLM_TeamMembership" WHERE team_id='team-it' AND user_id='user-it'`, nil},
		{`SELECT spend FROM "LiteLLM_OrganizationMembership" WHERE organization_id='org-it' AND user_id='user-it'`, nil},
		{`SELECT spend FROM "LiteLLM_EndUserTable" WHERE user_id='end-user-it'`, nil},
		{`SELECT spend FROM "LiteLLM_TagTable" WHERE tag_name='tag-it'`, nil},
		{`SELECT spend FROM "LiteLLM_AgentsTable" WHERE agent_id='agent-it'`, nil},
	}
	for _, check := range queries {
		var spend float64
		require.NoError(t, conn.QueryRow(ctx, check.query, check.args...).Scan(&spend), check.query)
		assert.InDelta(t, expected, spend, 1e-12, check.query)
	}
	for _, query := range []string{
		`SELECT COALESCE((model_spend ->> 'backend-it')::double precision, 0) FROM "LiteLLM_VerificationToken" WHERE token=$1`,
		`SELECT COALESCE((model_spend ->> 'backend-it')::double precision, 0) FROM "LiteLLM_UserTable" WHERE user_id='user-it'`,
		`SELECT COALESCE((model_spend ->> 'backend-it')::double precision, 0) FROM "LiteLLM_TeamTable" WHERE team_id='team-it'`,
		`SELECT COALESCE((model_spend ->> 'backend-it')::double precision, 0) FROM "LiteLLM_OrganizationTable" WHERE organization_id='org-it'`,
		`SELECT COALESCE((model_spend ->> 'backend-it')::double precision, 0) FROM "LiteLLM_ProjectTable" WHERE project_id='project-it'`,
	} {
		args := []any(nil)
		if strings.Contains(query, "VerificationToken") {
			args = []any{integrationKeyHash}
		}
		var spend float64
		require.NoError(t, conn.QueryRow(ctx, query, args...).Scan(&spend), query)
		assert.InDelta(t, expected, spend, 1e-12, query)
	}
}

func assertDailyDimensions(t *testing.T, ctx context.Context, conn *pgx.Conn, expectedRequests int) {
	t.Helper()
	for _, table := range []string{
		"LiteLLM_DailyUserSpend", "LiteLLM_DailyTeamSpend", "LiteLLM_DailyOrganizationSpend",
		"LiteLLM_DailyEndUserSpend", "LiteLLM_DailyAgentSpend", "LiteLLM_DailyTagSpend",
	} {
		query := `SELECT COALESCE(sum(api_requests),0), COALESCE(sum(successful_requests),0), COALESCE(sum(failed_requests),0) FROM ` + pgx.Identifier{table}.Sanitize()
		var requests, successful, failed int
		require.NoError(t, conn.QueryRow(ctx, query).Scan(&requests, &successful, &failed), table)
		assert.Equal(t, expectedRequests, requests, table)
		assert.Equal(t, expectedRequests-1, successful, table)
		assert.Equal(t, 1, failed, table)
	}
	var failureModel, failureModelGroup, failureProvider, failureMCP, failureEndpoint string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT model, model_group, custom_llm_provider,
		       COALESCE(mcp_namespaced_tool_name, ''), COALESCE(endpoint, '')
		FROM "LiteLLM_DailyUserSpend"
		WHERE failed_requests = 1
	`).Scan(&failureModel, &failureModelGroup, &failureProvider, &failureMCP, &failureEndpoint))
	assert.Equal(t, "public-it", failureModel)
	assert.Empty(t, failureModelGroup)
	assert.Empty(t, failureProvider)
	assert.Empty(t, failureMCP)
	assert.Empty(t, failureEndpoint)

	rows, err := conn.Query(ctx, `SELECT endpoint FROM "LiteLLM_DailyUserSpend" ORDER BY endpoint`)
	require.NoError(t, err)
	defer rows.Close()
	var endpoints []string
	for rows.Next() {
		var endpoint string
		require.NoError(t, rows.Scan(&endpoint))
		endpoints = append(endpoints, endpoint)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, []string{
		"", "/chat/completions", "/completions", "/embeddings", "/image/generations", "/images/edits",
	}, endpoints)
}
