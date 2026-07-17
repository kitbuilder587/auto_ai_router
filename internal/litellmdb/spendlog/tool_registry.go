package spendlog

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/utils"
)

const upsertToolRegistrySQL = `
INSERT INTO "LiteLLM_ToolTable" (
    tool_id,
    tool_name,
    origin,
    input_policy,
    output_policy,
    call_count,
    key_hash,
    team_id,
    key_alias,
    user_agent,
    last_used_at,
    created_by,
    updated_by
) VALUES ($1, $2, 'user_defined', 'untrusted', 'untrusted', 1, $3, $4, $5, $6, $7, 'system', 'system')
ON CONFLICT (tool_name) DO UPDATE SET
    call_count = "LiteLLM_ToolTable".call_count + 1,
    updated_at = EXCLUDED.last_used_at,
    last_used_at = EXCLUDED.last_used_at`

type toolRegistryExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type toolDiscovery struct {
	name     string
	keyHash  string
	teamID   string
	keyAlias string
}

// collectToolDiscoveries mirrors LiteLLM's queue-flush behavior: an exact,
// case-sensitive tool name is counted at most once per successfully inserted
// writer batch, and the first discovery supplies the immutable attribution.
func collectToolDiscoveries(batch []*models.SpendLogEntry) []toolDiscovery {
	seen := make(map[string]struct{})
	discoveries := make([]toolDiscovery, 0)
	for _, entry := range batch {
		if entry == nil {
			continue
		}
		for _, name := range entry.DeclaredToolNames {
			if name == "" {
				continue
			}
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			discoveries = append(discoveries, toolDiscovery{
				name:     name,
				keyHash:  entry.APIKey,
				teamID:   entry.TeamID,
				keyAlias: entry.ToolKeyAlias,
			})
		}
	}
	return discoveries
}

func upsertDiscoveredTools(ctx context.Context, execer toolRegistryExecer, batch []*models.SpendLogEntry) error {
	discoveries := collectToolDiscoveries(batch)
	if len(discoveries) == 0 {
		return nil
	}

	lastUsedAt := utils.NowUTC()
	for _, discovery := range discoveries {
		_, err := execer.Exec(
			ctx,
			upsertToolRegistrySQL,
			uuid.NewString(),
			discovery.name,
			toolRegistryNullable(discovery.keyHash),
			toolRegistryNullable(discovery.teamID),
			toolRegistryNullable(discovery.keyAlias),
			nil, // Pinned LiteLLM's observed request-discovery rows leave user_agent SQL NULL.
			lastUsedAt,
		)
		if err != nil {
			return fmt.Errorf("upsert tool %q: %w", discovery.name, err)
		}
	}
	return nil
}

func toolRegistryNullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
