package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mixaill76/auto_ai_router/internal/kafkalog"
)

// kafkaFallbackResendInterval mirrors kafkalog's own DLQ recovery cadence.
const kafkaFallbackResendInterval = 5 * time.Minute

// kafkaFallbackResendBatchLimit caps how many flagged rows are re-published per tick.
const kafkaFallbackResendBatchLimit = 200

// queryFetchPendingKafkaFallback selects LiteLLM_SpendLogs rows flagged by
// kafkalog (metadata.kafka_fallback=true, see internal/proxy/proxy_log.go and
// kafkalog.Config.FallbackNotifier) that haven't been successfully
// re-published to Kafka yet. COALESCE keeps every scanned column non-null so
// the resend loop can scan directly into plain Go types.
const queryFetchPendingKafkaFallback = `
	SELECT
		request_id,
		call_type,
		COALESCE(api_base, ''),
		model,
		COALESCE(model_id, ''),
		COALESCE(model_group, ''),
		COALESCE(custom_llm_provider, ''),
		prompt_tokens,
		completion_tokens,
		total_tokens,
		spend,
		api_key,
		COALESCE("user", ''),
		COALESCE(team_id, ''),
		COALESCE(organization_id, ''),
		COALESCE(end_user, ''),
		COALESCE(requester_ip_address, ''),
		COALESCE(session_id, ''),
		COALESCE(status, ''),
		"startTime",
		"endTime",
		COALESCE(metadata, '{}'::jsonb)
	FROM "LiteLLM_SpendLogs"
	WHERE metadata->>'kafka_fallback' = 'true'
	  AND metadata->>'kafka_fallback_resent' IS DISTINCT FROM 'true'
	ORDER BY "startTime"
	LIMIT $1
`

// queryMarkKafkaFallbackResent marks a row as successfully re-published so
// it isn't picked up again on the next tick.
const queryMarkKafkaFallbackResent = `
	UPDATE "LiteLLM_SpendLogs"
	SET metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object('kafka_fallback_resent', true)
	WHERE request_id = $1
`

// kafkaFallbackMetadata unmarshals only the pieces of buildMetadata's output
// (internal/proxy/proxy_helpers.go) needed to reconstruct a SpendEvent's cost
// and usage breakdown for resend.
type kafkaFallbackMetadata struct {
	UsageObject *struct {
		PromptTokensDetails struct {
			AudioTokens         int `json:"audio_tokens"`
			ImageTokens         int `json:"image_tokens"`
			CachedTokens        int `json:"cached_tokens"`
			CacheCreationTokens int `json:"cache_creation_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			AudioTokens              int `json:"audio_tokens"`
			ImageTokens              int `json:"image_tokens"`
			ReasoningTokens          int `json:"reasoning_tokens"`
			AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
			RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage_object"`
	CostBreakdown *struct {
		InputCost         float64 `json:"input_cost"`
		OutputCost        float64 `json:"output_cost"`
		CachedInputCost   float64 `json:"cached_input_cost"`
		CacheCreationCost float64 `json:"cache_creation_cost"`
		TotalCost         float64 `json:"total_cost"`
	} `json:"cost_breakdown"`
	UserAPIKeyAlias     string `json:"user_api_key_alias"`
	UserAPIKeyUserAlias string `json:"user_api_key_user_alias"`
	UserAPIKeyTeamAlias string `json:"user_api_key_team_alias"`
	ErrorInformation    *struct {
		ErrorMessage string `json:"error_message"`
		ErrorClass   string `json:"error_class"`
	} `json:"error_information"`
}

// startKafkaFallbackResendLoop periodically re-publishes LiteLLM_SpendLogs
// rows that kafkalog flagged after a Kafka queue-full or DLQ-overflow
// failure, closing the loop so those events eventually reach ClickHouse once
// Kafka recovers.
//
// Reconstruction is best-effort: fields with no Postgres equivalent
// (ServerRouterID/ServerVersion/ServerCommit, CredentialName/CredentialType/
// CredentialBaseURL, TTFT) are not restored — good enough for ClickHouse
// analytics continuity, not meant to be byte-identical to the original event.
func startKafkaFallbackResendLoop(
	log *slog.Logger,
	bgCtx context.Context,
	pool *pgxpool.Pool,
	kafkaLogManager kafkalog.Manager,
	wg *sync.WaitGroup,
) {
	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(kafkaFallbackResendInterval)
		defer ticker.Stop()

		for {
			select {
			case <-bgCtx.Done():
				log.Debug("Kafka fallback resend loop stopped")
				return
			case <-ticker.C:
				resendPendingKafkaFallbackSpendLogs(bgCtx, log, pool, kafkaLogManager)
			}
		}
	}()

	log.Info("Kafka fallback resend loop started", "interval", kafkaFallbackResendInterval)
}

func resendPendingKafkaFallbackSpendLogs(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, kafkaLogManager kafkalog.Manager) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("Panic in Kafka fallback resend loop", "panic", r)
		}
	}()

	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rows, err := pool.Query(queryCtx, queryFetchPendingKafkaFallback, kafkaFallbackResendBatchLimit)
	if err != nil {
		log.Warn("Kafka fallback resend: query failed", "error", err)
		return
	}
	defer rows.Close()

	resent := 0
	for rows.Next() {
		var (
			requestID, callType, apiBase, model, modelID, modelGroup, customLLMProvider string
			promptTokens, completionTokens, totalTokens                                 int
			spend                                                                       float64
			apiKey, userID, teamID, organizationID, endUser                             string
			requesterIP, sessionID, status                                              string
			startTime, endTime                                                          time.Time
			metadataRaw                                                                 []byte
		)
		if err := rows.Scan(&requestID, &callType, &apiBase, &model, &modelID, &modelGroup,
			&customLLMProvider, &promptTokens, &completionTokens, &totalTokens, &spend,
			&apiKey, &userID, &teamID, &organizationID, &endUser,
			&requesterIP, &sessionID, &status, &startTime, &endTime, &metadataRaw); err != nil {
			log.Warn("Kafka fallback resend: row scan failed", "error", err)
			continue
		}

		var meta kafkaFallbackMetadata
		_ = json.Unmarshal(metadataRaw, &meta)

		event := &kafkalog.SpendEvent{
			RequestID:        requestID,
			StartTime:        startTime,
			EndTime:          endTime,
			DurationMs:       endTime.Sub(startTime).Milliseconds(),
			CallType:         callType,
			APIBase:          apiBase,
			Status:           status,
			Model:            model,
			RealModel:        model,
			ModelID:          modelID,
			ModelGroup:       modelGroup,
			CredentialType:   customLLMProvider,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
			TotalCost:        spend,
			APIKeyHash:       apiKey,
			UserID:           userID,
			TeamID:           teamID,
			OrganizationID:   organizationID,
			EndUser:          endUser,
			RequesterIP:      requesterIP,
			SessionID:        sessionID,
			KeyAlias:         meta.UserAPIKeyAlias,
			UserAlias:        meta.UserAPIKeyUserAlias,
			TeamAlias:        meta.UserAPIKeyTeamAlias,
		}
		if meta.UsageObject != nil {
			pd := meta.UsageObject.PromptTokensDetails
			cd := meta.UsageObject.CompletionTokensDetails
			event.AudioInputTokens = pd.AudioTokens
			event.ImageTokens = pd.ImageTokens
			event.CachedInputTokens = pd.CachedTokens
			event.CacheCreationTokens = pd.CacheCreationTokens
			event.AudioOutputTokens = cd.AudioTokens
			event.OutputImageTokens = cd.ImageTokens
			event.ReasoningTokens = cd.ReasoningTokens
			event.AcceptedPredictionTokens = cd.AcceptedPredictionTokens
			event.RejectedPredictionTokens = cd.RejectedPredictionTokens
		}
		if meta.CostBreakdown != nil {
			event.InputCost = meta.CostBreakdown.InputCost
			event.OutputCost = meta.CostBreakdown.OutputCost
			event.CachedInputCost = meta.CostBreakdown.CachedInputCost
			event.CacheCreationCost = meta.CostBreakdown.CacheCreationCost
		}
		if meta.ErrorInformation != nil {
			event.ErrorMessage = meta.ErrorInformation.ErrorMessage
			event.ErrorClass = meta.ErrorInformation.ErrorClass
		}

		if err := kafkaLogManager.LogSpend(event); err != nil {
			// Kafka is still degraded (e.g. queue full again) - leave the flag
			// as-is, it will be retried on the next tick.
			log.Debug("Kafka fallback resend: re-publish failed, will retry next tick",
				"request_id", requestID, "error", err)
			continue
		}

		if _, err := pool.Exec(queryCtx, queryMarkKafkaFallbackResent, requestID); err != nil {
			log.Warn("Kafka fallback resend: failed to mark row as resent",
				"request_id", requestID, "error", err)
			continue
		}
		resent++
	}

	if resent > 0 {
		log.Info("Kafka fallback resend: re-published spend events", "count", resent)
	}
}
