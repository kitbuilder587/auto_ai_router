package spendlog

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/queries"
	"github.com/mixaill76/auto_ai_router/internal/utils"
)

type spendLogRecord struct {
	UserID                   string
	Date                     string
	APIKey                   string
	Model                    string
	ModelGroup               string
	CustomLLMProvider        string
	MCPNamespacedTool        string
	Endpoint                 string
	PromptTokens             int
	CompletionTokens         int
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	Spend                    float64
	Status                   string
	RequestID                string
	TeamID                   string
	OrganizationID           string
	EndUser                  string
	RequestTags              string
}

func loadUnprocessedSpendLogRecords(
	ctx context.Context,
	conn *pgxpool.Conn,
	logger *slog.Logger,
	scope string,
	requestIDs []string,
) ([]spendLogRecord, error) {
	rows, err := conn.Query(ctx, queries.QuerySelectUnprocessedSpendLogs, requestIDs)
	if err != nil {
		logger.Error("[DB] "+scope+" aggregation: failed to fetch spend logs", "error", err)
		return nil, err
	}
	defer rows.Close()

	records := make([]spendLogRecord, 0, len(requestIDs))
	for rows.Next() {
		var record spendLogRecord
		var userID *string
		var model, modelGroup, customLLMProvider, mcpNamespacedToolName, apiBase *string
		var status *string
		var teamID, organizationID, endUser, requestTags *string

		err := rows.Scan(
			&userID,
			&record.Date,
			&record.APIKey,
			&model,
			&modelGroup,
			&customLLMProvider,
			&mcpNamespacedToolName,
			&apiBase,
			&record.PromptTokens,
			&record.CompletionTokens,
			&record.CacheReadInputTokens,
			&record.CacheCreationInputTokens,
			&record.Spend,
			&status,
			&record.RequestID,
			&teamID,
			&organizationID,
			&endUser,
			&requestTags,
		)
		if err != nil {
			logger.Error("[DB] "+scope+" aggregation: failed to scan row", "error", err)
			return nil, err
		}

		record.UserID = derefString(userID)
		record.Model = derefString(model)
		record.ModelGroup = derefString(modelGroup)
		record.CustomLLMProvider = derefString(customLLMProvider)
		record.MCPNamespacedTool = derefString(mcpNamespacedToolName)
		record.Endpoint = derefString(apiBase)
		record.Status = derefString(status)
		record.TeamID = derefString(teamID)
		record.OrganizationID = derefString(organizationID)
		record.EndUser = derefString(endUser)
		record.RequestTags = derefString(requestTags)

		records = append(records, record)
	}

	if err := rows.Err(); err != nil {
		logger.Error("[DB] "+scope+" aggregation: failed to iterate rows", "error", err)
		return nil, err
	}

	return records, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// runAggregators runs all 6 daily aggregators on the given records.
// Returns true if all aggregators succeeded.
func (sl *Logger) runAggregators(aggCtx context.Context, conn *pgxpool.Conn, scope string, records []spendLogRecord) bool {
	hasErrors := false
	aggregators := []struct {
		name string
		fn   func() error
	}{
		{"User", func() error {
			c, cn := context.WithTimeout(aggCtx, 30*time.Second)
			defer cn()
			return aggregateDailyUserSpendLogs(c, conn, sl.logger, records)
		}},
		{"Team", func() error {
			c, cn := context.WithTimeout(aggCtx, 30*time.Second)
			defer cn()
			return aggregateDailyTeamSpendLogs(c, conn, sl.logger, records)
		}},
		{"Organization", func() error {
			c, cn := context.WithTimeout(aggCtx, 30*time.Second)
			defer cn()
			return aggregateDailyOrganizationSpendLogs(c, conn, sl.logger, records)
		}},
		{"EndUser", func() error {
			c, cn := context.WithTimeout(aggCtx, 30*time.Second)
			defer cn()
			return aggregateDailyEndUserSpendLogs(c, conn, sl.logger, records)
		}},
		{"Tag", func() error {
			c, cn := context.WithTimeout(aggCtx, 30*time.Second)
			defer cn()
			return aggregateDailyTagSpendLogs(c, conn, sl.logger, records)
		}},
	}

	for _, agg := range aggregators {
		if err := agg.fn(); err != nil {
			hasErrors = true
			atomic.AddUint64(&sl.aggregationErrors, 1)
			sl.logger.Error("[DB] "+scope+": aggregator failed", "aggregator", agg.name, "error", err)
		}
	}

	if !hasErrors {
		atomic.AddUint64(&sl.aggregationCount, 1)
		sl.mu.Lock()
		sl.lastAggregationTime = utils.NowUTC()
		sl.mu.Unlock()
		return true
	}
	return false
}

// aggregateByIDs aggregates specific logs (by request_id list) into all Daily tables.
// Called from aggregationWorker after receiving insertedIDs from pendingAggregation.
// No distributed lock needed — each router processes only its own IDs.
func (sl *Logger) aggregateByIDs(ids []string) {
	if len(ids) == 0 {
		return
	}

	aggCtx, aggCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer aggCancel()

	conn, err := sl.pool.Acquire(aggCtx)
	if err != nil {
		atomic.AddUint64(&sl.aggregationErrors, 1)
		sl.logger.Error("[DB] aggregateByIDs: failed to acquire connection", "error", err, "ids_count", len(ids))
		return
	}
	defer conn.Release()

	loadCtx, loadCancel := context.WithTimeout(aggCtx, 30*time.Second)
	records, err := loadUnprocessedSpendLogRecords(loadCtx, conn, sl.logger, "push", ids)
	loadCancel()
	if err != nil {
		atomic.AddUint64(&sl.aggregationErrors, 1)
		return
	}
	if len(records) == 0 {
		return
	}

	sl.runAggregators(aggCtx, conn, "aggregateByIDs", records)
}
