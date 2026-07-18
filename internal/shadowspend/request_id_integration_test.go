//go:build integration

package shadowspend

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShadowSinkConcurrentProviderResponseIDCollision(t *testing.T) {
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
	schemaName := "air_request_id_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	cfg := integrationSpendConfig(withSearchPath(t, baseDSN, schemaName), databaseName)
	cfg.LogBatchSize = 1
	cfg.LogFlushInterval = time.Hour
	firstSink, err := New(ctx, cfg, slog.Default())
	require.NoError(t, err)
	secondSink, err := New(ctx, cfg, slog.Default())
	require.NoError(t, err)

	identity := integrationIdentityFixture()
	providerID := "chatcmpl-concurrent-integration"
	first := collisionIntegrationEntry(identity, providerID, "air-event-concurrent-1", time.Now().UTC())
	second := collisionIntegrationEntry(identity, providerID, "air-event-concurrent-2", first.StartTime.Add(time.Millisecond))

	start := make(chan struct{})
	logErrors := make(chan error, 2)
	go func() {
		<-start
		logErrors <- firstSink.LogSpend(first)
	}()
	go func() {
		<-start
		logErrors <- secondSink.LogSpend(second)
	}()
	close(start)
	require.NoError(t, <-logErrors)
	require.NoError(t, <-logErrors)

	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 15*time.Second)
	defer shutdownCancel()
	shutdownErrors := make(chan error, 2)
	var shutdownWG sync.WaitGroup
	for _, sink := range []Sink{firstSink, secondSink} {
		shutdownWG.Add(1)
		go func(sink Sink) {
			defer shutdownWG.Done()
			shutdownErrors <- sink.Shutdown(shutdownCtx)
		}(sink)
	}
	shutdownWG.Wait()
	close(shutdownErrors)
	for shutdownErr := range shutdownErrors {
		require.NoError(t, shutdownErr)
	}

	rows, err := admin.Query(ctx, `
		SELECT request_id, COALESCE(metadata #>> '{spend_logs_metadata,air_event_id}', '')
		FROM "LiteLLM_SpendLogs"
		ORDER BY request_id`)
	require.NoError(t, err)
	defer rows.Close()
	requestIDs := make(map[string]struct{})
	eventIDs := make(map[string]struct{})
	for rows.Next() {
		var requestID, eventID string
		require.NoError(t, rows.Scan(&requestID, &eventID))
		requestIDs[requestID] = struct{}{}
		eventIDs[eventID] = struct{}{}
	}
	require.NoError(t, rows.Err())
	assert.Len(t, requestIDs, 2)
	assert.Contains(t, requestIDs, providerID)
	_, firstFallback := requestIDs[first.AirEventID]
	_, secondFallback := requestIDs[second.AirEventID]
	assert.NotEqual(t, firstFallback, secondFallback, "exactly one colliding effect uses its AIR event ID")
	assert.Equal(t, map[string]struct{}{first.AirEventID: {}, second.AirEventID: {}}, eventIDs)
	assertCounterIdempotency(t, ctx, admin, first.Spend+second.Spend)
	for _, table := range []string{
		"LiteLLM_DailyUserSpend", "LiteLLM_DailyTeamSpend", "LiteLLM_DailyOrganizationSpend",
		"LiteLLM_DailyEndUserSpend", "LiteLLM_DailyAgentSpend", "LiteLLM_DailyTagSpend",
	} {
		var requests int
		query := `SELECT COALESCE(sum(api_requests), 0) FROM ` + pgx.Identifier{table}.Sanitize()
		require.NoError(t, admin.QueryRow(ctx, query).Scan(&requests), table)
		assert.Equal(t, 2, requests, table)
	}
}

func collisionIntegrationEntry(
	identity integrationIdentity,
	providerID string,
	eventID string,
	start time.Time,
) *models.SpendLogEntry {
	metadata := fmt.Sprintf(`{
		"litellm_call_id":%q,
		"status":"success",
		"usage_object":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15},
		"spend_logs_metadata":{
			"comparison_eligible":true,
			"shadow_context_state":"valid",
			"usage_source":"provider",
			"air_event_id":%q,
			"provider_response_id":%q
		}
	}`, "call-"+eventID, eventID, providerID)
	return &models.SpendLogEntry{
		RequestID: providerID, AirEventID: eventID,
		StartTime: start, EndTime: start.Add(time.Millisecond), RequestDurationMS: 1,
		CallType: "acompletion", APIBase: "http://air-ru01/v1",
		Model: "backend-it", ModelID: identity.DeploymentID, ModelGroup: identity.PublicModel,
		CustomLLMProvider: "openai", PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
		Metadata: metadata, CacheHit: "False", CacheKey: "Cache OFF", Spend: 0.001,
		APIKey: identity.APIKeyHash, UserID: identity.UserID, TeamID: identity.TeamID,
		OrganizationID: identity.OrganizationID, ProjectID: identity.ProjectID, EndUser: identity.EndUser,
		RequesterIP: "192.0.2.10", SessionID: "session-" + eventID,
		RequestTags: `["tag-it"]`, AgentID: identity.AgentID, Status: "success",
		ComparisonEligible: true,
	}
}
