package spendlog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mixaill76/auto_ai_router/internal/litellmdb/queries"
)

// aggregateTagKey represents unique tag spend log grouping dimension
type aggregateTagKey struct {
	tag                   string
	date                  string
	apiKey                string
	model                 string
	modelGroup            string
	customLLMProvider     string
	mcpNamespacedToolName string
	endpoint              string
}

func (k aggregateTagKey) lockOrder() string {
	return strings.Join([]string{k.tag, k.date, k.apiKey, k.model, k.modelGroup,
		k.customLLMProvider, k.mcpNamespacedToolName, k.endpoint}, "\x00")
}

// aggregateTagValue holds aggregated metrics for a tag with request_id
type aggregateTagValue struct {
	aggregationValue
	requestID string // Store the first request_id for the tag (required by schema)
}

// aggregateDailyTagSpendLogs aggregates spend logs into DailyTagSpend
//
// This function:
// 1. Fetches spend logs from SpendLogs table filtered by requestIDs
// 2. For each log, parses request_tags JSON array
// 3. Groups by (tag, date, api_key, model, provider, mcp_tool, endpoint)
// 4. Sums tokens, spend, and request counts per group
// 5. UPSERTs aggregated data into DailyTagSpend table
//
// Returns nil if successful (including "no logs to aggregate" case).
// Returns error on any database operation failure.
func aggregateDailyTagSpendLogs(
	ctx context.Context,
	conn dailySpendExecer,
	logger *slog.Logger,
	records []spendLogRecord,
) error {
	// Map to aggregate by unique key
	aggregations := make(map[aggregateTagKey]*aggregateTagValue)
	totalRows := 0
	skippedRows := 0

	for _, record := range records {
		totalRows++

		tags, err := parseUniqueRequestTags(record.RequestTags)
		if err != nil {
			logger.Error("[DB] Tag aggregation: failed to unmarshal request_tags JSON",
				"request_id", record.RequestID,
				"error", err,
			)
			return fmt.Errorf("parse request_tags for %s: %w", record.RequestID, err)
		}
		if len(tags) == 0 {
			skippedRows++
			continue
		}

		// Each tag contributes at most once per request, matching counter semantics.
		for _, tag := range tags {
			if tag == "" {
				continue
			}

			key := aggregateTagKey{
				tag:                   tag,
				date:                  record.Date,
				apiKey:                record.APIKey,
				model:                 record.Model,
				modelGroup:            record.ModelGroup,
				customLLMProvider:     record.CustomLLMProvider,
				mcpNamespacedToolName: record.MCPNamespacedTool,
				endpoint:              record.Endpoint,
			}

			if aggregations[key] == nil {
				aggregations[key] = &aggregateTagValue{
					requestID: record.RequestID, // Store first request_id
				}
			}

			aggregations[key].addRecord(record)
		}
	}

	logger.Debug("[DB] Tag aggregation: scan complete",
		"total_rows", totalRows,
		"skipped_rows", skippedRows,
		"aggregation_groups", len(aggregations),
	)

	if len(aggregations) == 0 {
		return nil
	}

	// Insert aggregated data into DailyTagSpend
	for _, key := range sortedDailyKeys(aggregations) {
		value := aggregations[key]
		_, err := conn.Exec(ctx,
			queries.QueryUpsertDailyTagSpend,
			key.tag, value.requestID, key.date, key.apiKey, key.model, key.modelGroup,
			key.customLLMProvider, key.mcpNamespacedToolName, key.endpoint,
			value.promptTokens, value.completionTokens,
			value.cacheReadInputTokens, value.cacheCreationInputTokens,
			value.spend,
			value.apiRequests, value.successfulRequests, value.failedRequests,
		)

		if err != nil {
			logger.Error("[DB] Tag aggregation: failed to upsert daily spend", "error", err, "key", key)
			return err
		}
	}

	logger.Debug("[DB] Tag aggregation completed",
		"aggregations", len(aggregations),
	)

	return nil
}
