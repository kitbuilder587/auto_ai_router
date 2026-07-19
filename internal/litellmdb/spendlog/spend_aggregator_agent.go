package spendlog

import (
	"context"
	"log/slog"
	"strings"

	"github.com/mixaill76/auto_ai_router/internal/litellmdb/queries"
)

type aggregateAgentKey struct {
	agentID               string
	date                  string
	apiKey                string
	model                 string
	modelGroup            string
	customLLMProvider     string
	mcpNamespacedToolName string
	endpoint              string
}

func (k aggregateAgentKey) lockOrder() string {
	return strings.Join([]string{k.agentID, k.date, k.apiKey, k.model, k.modelGroup,
		k.customLLMProvider, k.mcpNamespacedToolName, k.endpoint}, "\x00")
}

func aggregateDailyAgentSpendLogs(
	ctx context.Context,
	conn dailySpendExecer,
	logger *slog.Logger,
	records []spendLogRecord,
) error {
	aggregations := make(map[aggregateAgentKey]*aggregationValue)
	for _, record := range records {
		if record.AgentID == "" {
			continue
		}
		key := aggregateAgentKey{
			agentID:               record.AgentID,
			date:                  record.Date,
			apiKey:                record.APIKey,
			model:                 record.Model,
			modelGroup:            record.ModelGroup,
			customLLMProvider:     record.CustomLLMProvider,
			mcpNamespacedToolName: record.MCPNamespacedTool,
			endpoint:              record.Endpoint,
		}
		if aggregations[key] == nil {
			aggregations[key] = &aggregationValue{}
		}
		aggregations[key].addRecord(record)
	}

	for _, key := range sortedDailyKeys(aggregations) {
		value := aggregations[key]
		_, err := conn.Exec(ctx,
			queries.QueryUpsertDailyAgentSpend,
			key.agentID, key.date, key.apiKey, key.model, key.modelGroup,
			key.customLLMProvider, key.mcpNamespacedToolName, key.endpoint,
			value.promptTokens, value.completionTokens,
			value.cacheReadInputTokens, value.cacheCreationInputTokens,
			value.spend, value.apiRequests, value.successfulRequests, value.failedRequests,
		)
		if err != nil {
			logger.Error("[DB] Agent aggregation: failed to upsert daily spend", "error", err, "key", key)
			return err
		}
	}
	return nil
}
