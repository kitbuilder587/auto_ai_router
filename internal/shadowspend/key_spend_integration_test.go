//go:build integration

package shadowspend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/spendlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSynchronousKeySpendSerializesAcrossShadowWriterInstances(t *testing.T) {
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
	schemaName := "air_key_spend_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	first := firstSink.(*ShadowSink)
	second := secondSink.(*ShadowSink)
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = firstSink.Shutdown(shutdownCtx)
		_ = secondSink.Shutdown(shutdownCtx)
	})

	initialSpend, known, err := first.logger.ReadKeySpend(ctx, integrationKeyHash)
	require.NoError(t, err)
	require.True(t, known)

	identity := integrationIdentityFixture()
	const requestCount = 8
	entries := make([]*models.SpendLogEntry, 0, requestCount)
	startedAt := time.Now().UTC()
	for index := 0; index < requestCount; index++ {
		entries = append(entries, collisionIntegrationEntry(
			identity,
			fmt.Sprintf("chatcmpl-key-spend-%d", index),
			fmt.Sprintf("air-event-key-spend-%d", index),
			startedAt.Add(time.Duration(index)*time.Millisecond),
		))
	}

	type commitOutcome struct {
		result spendlog.CommitResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan commitOutcome, requestCount)
	var wg sync.WaitGroup
	for index, entry := range entries {
		logger := first.logger
		if index%2 == 1 {
			logger = second.logger
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, commitErr := logger.CommitSpend(ctx, entry)
			outcomes <- commitOutcome{result: result, err: commitErr}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	returnedSpends := make([]float64, 0, requestCount)
	for outcome := range outcomes {
		require.NoError(t, outcome.err)
		assert.True(t, outcome.result.Inserted)
		assert.NotEmpty(t, outcome.result.EffectiveRequestID)
		require.True(t, outcome.result.KeySpendKnown)
		returnedSpends = append(returnedSpends, outcome.result.KeySpend)
	}
	require.Len(t, returnedSpends, requestCount)
	sort.Float64s(returnedSpends)
	for index, value := range returnedSpends {
		assert.InDelta(t, initialSpend+float64(index+1)*entries[index].Spend, value, 1e-12)
	}

	finalSpend, known, err := second.logger.ReadKeySpend(ctx, integrationKeyHash)
	require.NoError(t, err)
	require.True(t, known)
	assert.InDelta(t, initialSpend+float64(requestCount)*entries[0].Spend, finalSpend, 1e-12)

	replay, err := second.logger.CommitSpend(ctx, entries[0])
	require.NoError(t, err)
	assert.False(t, replay.Inserted)
	assert.Empty(t, replay.EffectiveRequestID)
	require.True(t, replay.KeySpendKnown)
	assert.InDelta(t, finalSpend, replay.KeySpend, 1e-12)

	var rawRows int
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM "LiteLLM_SpendLogs"`).Scan(&rawRows))
	assert.Equal(t, requestCount, rawRows)

	firstStats := first.logger.Stats()
	secondStats := second.logger.Stats()
	assert.Equal(t, uint64(requestCount+1), firstStats.Queued+secondStats.Queued)
	assert.Equal(t, uint64(requestCount), firstStats.Written+secondStats.Written)
	assert.Equal(t, uint64(1), firstStats.Duplicates+secondStats.Duplicates)
	assert.Zero(t, firstStats.PendingEntries)
	assert.Zero(t, secondStats.PendingEntries)

	// A foreign writer can legally hold the same token row longer than the
	// response budget. PostgreSQL's plain statement snapshot must remain
	// readable while the synchronous inclusive transaction waits on that row.
	// On deadline, CommitSpend retains the exact event for idempotent replay;
	// callers may safely expose only the earlier committed snapshot.
	blocker, err := pgx.Connect(ctx, withSearchPath(t, baseDSN, schemaName))
	require.NoError(t, err)
	defer func() { require.NoError(t, blocker.Close(context.Background())) }()
	blockerTx, err := blocker.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blockerTx.Rollback(context.Background()) }()

	const externalDelta = 0.01
	_, err = blockerTx.Exec(ctx, `
		UPDATE "LiteLLM_VerificationToken"
		SET spend = spend + $1
		WHERE token = $2`, externalDelta, integrationKeyHash)
	require.NoError(t, err)

	preRequestSnapshot, known, err := second.logger.ReadKeySpend(ctx, integrationKeyHash)
	require.NoError(t, err)
	require.True(t, known)
	assert.InDelta(t, finalSpend, preRequestSnapshot, 1e-12,
		"an uncommitted external update must not leak into the statement snapshot")

	blockedEntry := collisionIntegrationEntry(
		identity,
		"chatcmpl-key-spend-blocked",
		"air-event-key-spend-blocked",
		startedAt.Add(time.Second),
	)
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	startedCommit := time.Now()
	blockedResult, blockedErr := first.logger.CommitSpend(deadlineCtx, blockedEntry)
	deadlineCancel()
	require.Error(t, blockedErr)
	assert.True(t, errors.Is(blockedErr, context.DeadlineExceeded), blockedErr)
	assert.True(t, blockedResult.ReplayRetained,
		"the logger must own the exact replay before releasing its lifecycle ticket")
	assert.Less(t, time.Since(startedCommit), 2*time.Second,
		"foreign row-lock contention must remain bounded by the caller deadline")

	whileLocked, known, err := second.logger.ReadKeySpend(ctx, integrationKeyHash)
	require.NoError(t, err)
	require.True(t, known)
	assert.InDelta(t, preRequestSnapshot, whileLocked, 1e-12,
		"fallback is a committed PostgreSQL snapshot, never the unacknowledged attempted total")

	require.NoError(t, blockerTx.Commit(ctx))
	expectedAfterReplay := preRequestSnapshot + externalDelta + blockedEntry.Spend
	require.Eventually(t, func() bool {
		value, valueKnown, readErr := second.logger.ReadKeySpend(ctx, integrationKeyHash)
		return readErr == nil && valueKnown && math.Abs(value-expectedAfterReplay) <= 1e-12
	}, 5*time.Second, 20*time.Millisecond)
	assert.LessOrEqual(t, preRequestSnapshot, expectedAfterReplay,
		"a later external commit may make the fallback stale, but never fabricated or greater than DB truth")

	retainedReplay, err := second.logger.CommitSpend(ctx, blockedEntry)
	require.NoError(t, err)
	assert.False(t, retainedReplay.Inserted, "the retained event must have committed exactly once")
	assert.InDelta(t, expectedAfterReplay, retainedReplay.KeySpend, 1e-12)
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM "LiteLLM_SpendLogs"`).Scan(&rawRows))
	assert.Equal(t, requestCount+1, rawRows)
}

func TestDailyProjectionDeadlineReplaysWithoutPoisoningTerminalWindow(t *testing.T) {
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
	schemaName := "air_daily_replay_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	dsn := withSearchPath(t, baseDSN, schemaName)
	cfg := integrationSpendConfig(dsn, databaseName)
	cfg.LogBatchSize = 1
	cfg.LogFlushInterval = 10 * time.Millisecond
	sink, err := New(ctx, cfg, slog.Default())
	require.NoError(t, err)
	shadowSink := sink.(*ShadowSink)
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = sink.Shutdown(shutdownCtx)
	})

	identity := integrationIdentityFixture()
	startedAt := time.Now().UTC()
	seedEntry := collisionIntegrationEntry(
		identity,
		"chatcmpl-daily-replay-seed",
		"air-event-daily-replay-seed",
		startedAt,
	)
	seedResult, err := shadowSink.logger.CommitSpend(ctx, seedEntry)
	require.NoError(t, err)
	require.True(t, seedResult.Inserted)

	blocker, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, blocker.Close(context.Background())) }()
	blockerTx, err := blocker.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blockerTx.Rollback(context.Background()) }()
	commandTag, err := blockerTx.Exec(ctx, `
		UPDATE "LiteLLM_DailyUserSpend"
		SET spend = spend
		WHERE user_id = 'user-it' AND api_key = $1`, integrationKeyHash)
	require.NoError(t, err)
	require.EqualValues(t, 1, commandTag.RowsAffected())

	blockedEntry := collisionIntegrationEntry(
		identity,
		"chatcmpl-daily-replay-blocked",
		"air-event-daily-replay-blocked",
		startedAt.Add(time.Second),
	)
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	blockedResult, blockedErr := shadowSink.logger.CommitSpend(deadlineCtx, blockedEntry)
	deadlineCancel()
	require.Error(t, blockedErr)
	assert.True(t, errors.Is(blockedErr, context.DeadlineExceeded), blockedErr)
	assert.True(t, blockedResult.ReplayRetained)
	blockedStats := shadowSink.logger.Stats()
	assert.Equal(t, 1, blockedStats.PendingEntries)
	assert.Zero(t, blockedStats.AggregationErrors,
		"a rolled-back daily attempt remains recoverable while its exact replay is pending")
	assert.False(t, blockedStats.ComparisonWindowValid)

	require.NoError(t, blockerTx.Commit(ctx))
	require.Eventually(t, func() bool {
		stats := shadowSink.logger.Stats()
		return stats.PendingEntries == 0 &&
			stats.DLQSize == 0 &&
			stats.AggregationErrors == 0 &&
			stats.ComparisonWindowValid
	}, 10*time.Second, 20*time.Millisecond)

	var rawRows int
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM "LiteLLM_SpendLogs"`).Scan(&rawRows))
	assert.Equal(t, 2, rawRows)
	assertCounterIdempotency(t, ctx, admin, seedEntry.Spend+blockedEntry.Spend)
	for _, table := range []string{
		"LiteLLM_DailyUserSpend", "LiteLLM_DailyTeamSpend", "LiteLLM_DailyOrganizationSpend",
		"LiteLLM_DailyEndUserSpend", "LiteLLM_DailyAgentSpend", "LiteLLM_DailyTagSpend",
	} {
		query := `SELECT COALESCE(sum(api_requests),0), COALESCE(sum(successful_requests),0), ` +
			`COALESCE(sum(failed_requests),0), COALESCE(sum(spend),0) FROM ` + pgx.Identifier{table}.Sanitize()
		var requests, successful, failed int
		var spend float64
		require.NoError(t, admin.QueryRow(ctx, query).Scan(&requests, &successful, &failed, &spend), table)
		assert.Equal(t, 2, requests, table)
		assert.Equal(t, 2, successful, table)
		assert.Zero(t, failed, table)
		assert.InDelta(t, seedEntry.Spend+blockedEntry.Spend, spend, 1e-12, table)
	}

	replay, err := shadowSink.logger.CommitSpend(ctx, blockedEntry)
	require.NoError(t, err)
	assert.False(t, replay.Inserted)
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM "LiteLLM_SpendLogs"`).Scan(&rawRows))
	assert.Equal(t, 2, rawRows)
}
