package spendlog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectToolDiscoveriesDeduplicatesExactNamesAcrossBatch(t *testing.T) {
	discoveries := collectToolDiscoveries([]*models.SpendLogEntry{
		{
			APIKey:            "first-key",
			TeamID:            "first-team",
			ToolKeyAlias:      "first-alias",
			DeclaredToolNames: []string{"weather", "local_time", "weather", ""},
		},
		{
			APIKey:            "second-key",
			TeamID:            "second-team",
			ToolKeyAlias:      "second-alias",
			DeclaredToolNames: []string{"weather", "Weather"},
		},
	})

	require.Len(t, discoveries, 3)
	assert.Equal(t, toolDiscovery{name: "weather", keyHash: "first-key", teamID: "first-team", keyAlias: "first-alias"}, discoveries[0])
	assert.Equal(t, toolDiscovery{name: "local_time", keyHash: "first-key", teamID: "first-team", keyAlias: "first-alias"}, discoveries[1])
	assert.Equal(t, toolDiscovery{name: "Weather", keyHash: "second-key", teamID: "second-team", keyAlias: "second-alias"}, discoveries[2])
}

func TestUpsertDiscoveredToolsUsesPinnedLiteLLMConflictSemantics(t *testing.T) {
	recorder := &recordingToolRegistryExecer{}
	batch := []*models.SpendLogEntry{{
		APIKey:            "key-hash",
		TeamID:            "team-1",
		ToolKeyAlias:      "fixture-key",
		DeclaredToolNames: []string{"weather", "local_time", "weather"},
	}}

	require.NoError(t, upsertDiscoveredTools(context.Background(), recorder, batch))
	require.Len(t, recorder.calls, 2)

	first := recorder.calls[0]
	second := recorder.calls[1]
	assert.Equal(t, upsertToolRegistrySQL, first.sql)
	assert.Contains(t, first.sql, `'user_defined', 'untrusted', 'untrusted', 1`)
	assert.Contains(t, first.sql, `ON CONFLICT (tool_name) DO UPDATE SET`)
	assert.Contains(t, first.sql, `call_count = "LiteLLM_ToolTable".call_count + 1`)
	assert.Contains(t, first.sql, `updated_at = EXCLUDED.last_used_at`)
	assert.NotContains(t, first.sql, "assignments", "the database default must create assignments as an empty object")
	require.Len(t, first.args, 7)
	require.Len(t, second.args, 7)
	_, err := uuid.Parse(first.args[0].(string))
	require.NoError(t, err)
	_, err = uuid.Parse(second.args[0].(string))
	require.NoError(t, err)
	// Upserts run in sorted tool-name order (stable cross-replica lock order).
	assert.Equal(t, "local_time", first.args[1])
	assert.Equal(t, "weather", second.args[1])
	assert.Equal(t, "key-hash", first.args[2])
	assert.Equal(t, "team-1", first.args[3])
	assert.Equal(t, "fixture-key", first.args[4])
	assert.Nil(t, first.args[5], "observed pinned LiteLLM request discovery stores SQL NULL user_agent")
	firstTime, ok := first.args[6].(time.Time)
	require.True(t, ok)
	secondTime, ok := second.args[6].(time.Time)
	require.True(t, ok)
	assert.Equal(t, firstTime, secondTime, "one flush cycle uses one last_used_at value")
	assert.Equal(t, time.UTC, firstTime.Location())
}

func TestUpsertDiscoveredToolsUsesSQLNullForMissingIdentity(t *testing.T) {
	recorder := &recordingToolRegistryExecer{}
	require.NoError(t, upsertDiscoveredTools(context.Background(), recorder, []*models.SpendLogEntry{{
		DeclaredToolNames: []string{"weather"},
	}}))

	require.Len(t, recorder.calls, 1)
	assert.Nil(t, recorder.calls[0].args[2])
	assert.Nil(t, recorder.calls[0].args[3])
	assert.Nil(t, recorder.calls[0].args[4])
	assert.Nil(t, recorder.calls[0].args[5])
}

func TestUpsertDiscoveredToolsStopsOnFirstDatabaseError(t *testing.T) {
	recorder := &recordingToolRegistryExecer{failAt: 1}
	err := upsertDiscoveredTools(context.Background(), recorder, []*models.SpendLogEntry{{
		DeclaredToolNames: []string{"weather", "local_time"},
	}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `upsert tool "weather"`)
	assert.Len(t, recorder.calls, 2)
}

type toolRegistryExecCall struct {
	sql  string
	args []any
}

type recordingToolRegistryExecer struct {
	calls  []toolRegistryExecCall
	failAt int
}

func (recorder *recordingToolRegistryExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	recorder.calls = append(recorder.calls, toolRegistryExecCall{sql: sql, args: append([]any(nil), args...)})
	if recorder.failAt > 0 && len(recorder.calls)-1 == recorder.failAt {
		return pgconn.CommandTag{}, errors.New("injected tool registry failure")
	}
	return pgconn.CommandTag{}, nil
}
