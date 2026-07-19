package spendlog

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/queries"
)

// requestIDGroup contains all logical effects that prefer the same
// provider-controlled response ID. The first effect gets the preferred ID when
// it is free; every other distinct event falls back to its AIR event ID.
type requestIDGroup struct {
	preferredID    string
	representative *models.SpendLogEntry
	entries        []*models.SpendLogEntry
}

// insertSpendRowsCollisionSafe inserts each logical effect exactly once while
// preserving LiteLLM's normal request_id shape. The owner lookup deliberately
// happens in a separate SQL statement: INSERT ... ON CONFLICT may wait for a
// concurrent transaction that is invisible to the INSERT statement snapshot,
// while the next READ COMMITTED statement sees the committed winner.
func insertSpendRowsCollisionSafe(ctx context.Context, tx pgx.Tx, batch []*models.SpendLogEntry) ([]string, error) {
	groups := groupEntriesByPreferredRequestID(batch)
	if len(groups) == 0 {
		return nil, nil
	}

	representatives := make([]*models.SpendLogEntry, 0, len(groups))
	for _, group := range groups {
		representatives = append(representatives, cloneEntryWithRequestID(group.representative, group.preferredID))
	}
	preferredInserted, err := insertSpendRowsReturningIDs(ctx, tx, representatives)
	if err != nil {
		return nil, fmt.Errorf("batch insert preferred request IDs: %w", err)
	}
	preferredInsertedSet := stringSet(preferredInserted)

	conflictedIDs := make([]string, 0, len(groups))
	for _, group := range groups {
		if _, inserted := preferredInsertedSet[group.preferredID]; inserted {
			continue
		}
		if groupHasEventFallback(group) {
			conflictedIDs = append(conflictedIDs, group.preferredID)
		}
	}
	ownerByPreferredID, err := selectSpendRowEventOwners(ctx, tx, conflictedIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve conflicting request ID owners: %w", err)
	}

	fallbacks := make([]*models.SpendLogEntry, 0)
	fallbackSeen := make(map[string]struct{})
	for _, group := range groups {
		ownerEventID := ownerByPreferredID[group.preferredID]
		_, preferredWasInserted := preferredInsertedSet[group.preferredID]
		if preferredWasInserted {
			ownerEventID = group.representative.AirEventID
		}

		for _, entry := range group.entries {
			eventID := entry.AirEventID
			if eventID == "" || eventID == ownerEventID {
				continue
			}
			// A legacy representative without an AIR event ID was already
			// inserted under the preferred ID and has no fallback to add.
			if preferredWasInserted && entry == group.representative {
				continue
			}
			if _, duplicateEvent := fallbackSeen[eventID]; duplicateEvent {
				continue
			}
			fallbackSeen[eventID] = struct{}{}
			fallbacks = append(fallbacks, cloneEntryWithRequestID(entry, eventID))
		}
	}
	sort.Slice(fallbacks, func(i, j int) bool {
		return fallbacks[i].RequestID < fallbacks[j].RequestID
	})

	fallbackInserted, err := insertSpendRowsReturningIDs(ctx, tx, fallbacks)
	if err != nil {
		return nil, fmt.Errorf("batch insert AIR event ID fallbacks: %w", err)
	}
	return append(preferredInserted, fallbackInserted...), nil
}

func groupEntriesByPreferredRequestID(batch []*models.SpendLogEntry) []*requestIDGroup {
	byID := make(map[string]*requestIDGroup, len(batch))
	groups := make([]*requestIDGroup, 0, len(batch))
	for _, entry := range batch {
		if entry == nil {
			continue
		}
		preferredID := entry.RequestID
		if preferredID == "" {
			preferredID = entry.AirEventID
		}
		group := byID[preferredID]
		if group == nil {
			group = &requestIDGroup{preferredID: preferredID, representative: entry}
			byID[preferredID] = group
			groups = append(groups, group)
		}
		group.entries = append(group.entries, entry)
	}
	// Every transaction acquires unique-index locks in the same request_id
	// order. Without this, concurrent batches [P,Q] and [Q,P] can deadlock even
	// though each individual INSERT uses ON CONFLICT DO NOTHING.
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].preferredID < groups[j].preferredID
	})
	return groups
}

func groupHasEventFallback(group *requestIDGroup) bool {
	for _, entry := range group.entries {
		if entry.AirEventID != "" {
			return true
		}
	}
	return false
}

func cloneEntryWithRequestID(entry *models.SpendLogEntry, requestID string) *models.SpendLogEntry {
	clone := *entry
	clone.RequestID = requestID
	return &clone
}

func insertSpendRowsReturningIDs(ctx context.Context, tx pgx.Tx, entries []*models.SpendLogEntry) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, queries.BuildBatchInsertQuery(len(entries)), GetBatchParams(entries)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	insertedIDs := make([]string, 0, len(entries))
	for rows.Next() {
		var requestID string
		if err := rows.Scan(&requestID); err != nil {
			return nil, fmt.Errorf("scan returning request_id: %w", err)
		}
		insertedIDs = append(insertedIDs, requestID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate returning rows: %w", err)
	}
	return insertedIDs, nil
}

func selectSpendRowEventOwners(ctx context.Context, tx pgx.Tx, requestIDs []string) (map[string]string, error) {
	owners := make(map[string]string, len(requestIDs))
	if len(requestIDs) == 0 {
		return owners, nil
	}
	rows, err := tx.Query(ctx, queries.QuerySelectSpendLogEventOwners, requestIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var requestID, eventID string
		if err := rows.Scan(&requestID, &eventID); err != nil {
			return nil, err
		}
		owners[requestID] = eventID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return owners, nil
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
