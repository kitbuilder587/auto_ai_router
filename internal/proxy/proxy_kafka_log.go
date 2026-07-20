package proxy

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/converter"
	"github.com/mixaill76/auto_ai_router/internal/kafkalog"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb"
)

// logSpendToKafka publishes an expanded copy of the spend entry to Kafka
// (internal/kafkalog) for downstream ClickHouse analytics. Best-effort in the
// sense that Kafka availability never affects request processing or blocks
// the caller beyond kafkalog's own bounded wait (kafkalog.Manager.IsHealthy
// reflects broker connectivity independently, see
// auto_ai_router_kafka_spend_log_tz.md section 6) — but the returned error is
// still surfaced to the caller so a queue-full failure can be flagged on the
// request's shadow Postgres row for later re-send, instead of being silently
// dropped.
func (p *Proxy) logSpendToKafka(
	logCtx *RequestLogContext,
	credName, modelIDFormatted, hashedToken string,
	userID, teamID, organizationID, endUser, apiBase, status string,
	cost float64,
	tokenCosts *converter.TokenCosts,
	overheadMs float64,
	endTime time.Time,
) error {
	event := p.buildKafkaSpendEvent(logCtx, credName, modelIDFormatted, hashedToken,
		userID, teamID, organizationID, endUser, apiBase, status,
		cost, tokenCosts, overheadMs, endTime)

	if err := p.kafkaLog.LogSpend(event); err != nil {
		p.logger.WarnContext(logCtx.Context(), "Failed to queue Kafka spend event",
			"error", err,
			"request_id", logCtx.RequestID,
		)
		return err
	}
	return nil
}

type shadowKafkaMetadata struct {
	UsageObject struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		ImageCount       int `json:"image_count"`
		PromptDetails    struct {
			AudioTokens         int `json:"audio_tokens"`
			ImageTokens         int `json:"image_tokens"`
			CachedTokens        int `json:"cached_tokens"`
			CacheCreationTokens int `json:"cache_creation_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			AudioTokens              int `json:"audio_tokens"`
			CachedTokens             int `json:"cached_tokens"`
			ImageTokens              int `json:"image_tokens"`
			ReasoningTokens          int `json:"reasoning_tokens"`
			AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
			RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage_object"`
	CostBreakdown *struct {
		InputCost         float64 `json:"input_cost"`
		CacheReadCost     float64 `json:"cache_read_cost"`
		CacheCreationCost float64 `json:"cache_creation_cost"`
		OutputCost        float64 `json:"output_cost"`
		ReasoningCost     float64 `json:"reasoning_cost"`
		AudioInputCost    float64 `json:"audio_input_cost"`
		AudioOutputCost   float64 `json:"audio_output_cost"`
		PredictionCost    float64 `json:"prediction_cost"`
		CachedOutputCost  float64 `json:"cached_output_cost"`
		InputImageCost    float64 `json:"input_image_cost"`
		OutputImageCost   float64 `json:"output_image_cost"`
		ImageCost         float64 `json:"image_cost"`
		TotalCost         float64 `json:"total_cost"`
	} `json:"cost_breakdown"`
}

func parseShadowKafkaMetadata(entry *litellmdb.SpendLogEntry) (converter.TokenUsage, *converter.TokenCosts) {
	usage := converter.TokenUsage{}
	if entry != nil {
		usage.PromptTokens = entry.PromptTokens
		usage.CompletionTokens = entry.CompletionTokens
	}
	if entry == nil || entry.Metadata == "" {
		return usage, nil
	}

	var metadata shadowKafkaMetadata
	if err := json.Unmarshal([]byte(entry.Metadata), &metadata); err != nil {
		return usage, nil
	}
	usage.PromptTokens = metadata.UsageObject.PromptTokens
	usage.CompletionTokens = metadata.UsageObject.CompletionTokens
	usage.ImageCount = metadata.UsageObject.ImageCount
	usage.AudioInputTokens = metadata.UsageObject.PromptDetails.AudioTokens
	usage.ImageTokens = metadata.UsageObject.PromptDetails.ImageTokens
	usage.CachedInputTokens = metadata.UsageObject.PromptDetails.CachedTokens
	usage.CacheCreationTokens = metadata.UsageObject.PromptDetails.CacheCreationTokens
	usage.AudioOutputTokens = metadata.UsageObject.CompletionDetails.AudioTokens
	usage.CachedOutputTokens = metadata.UsageObject.CompletionDetails.CachedTokens
	usage.OutputImageTokens = metadata.UsageObject.CompletionDetails.ImageTokens
	usage.ReasoningTokens = metadata.UsageObject.CompletionDetails.ReasoningTokens
	usage.AcceptedPredictionTokens = metadata.UsageObject.CompletionDetails.AcceptedPredictionTokens
	usage.RejectedPredictionTokens = metadata.UsageObject.CompletionDetails.RejectedPredictionTokens

	breakdown := metadata.CostBreakdown
	if breakdown == nil {
		return usage, nil
	}
	inputCost := breakdown.InputCost - breakdown.AudioInputCost - breakdown.InputImageCost
	outputCost := breakdown.OutputCost - breakdown.AudioOutputCost - breakdown.ReasoningCost -
		breakdown.CachedOutputCost - breakdown.PredictionCost - breakdown.OutputImageCost
	return usage, &converter.TokenCosts{
		InputCost:         inputCost,
		OutputCost:        outputCost,
		AudioInputCost:    breakdown.AudioInputCost,
		AudioOutputCost:   breakdown.AudioOutputCost,
		ReasoningCost:     breakdown.ReasoningCost,
		CachedInputCost:   breakdown.CacheReadCost,
		CacheCreationCost: breakdown.CacheCreationCost,
		CachedOutputCost:  breakdown.CachedOutputCost,
		PredictionCost:    breakdown.PredictionCost,
		InputImageCost:    breakdown.InputImageCost,
		OutputImageCost:   breakdown.OutputImageCost,
		ImageCost:         breakdown.ImageCost,
		TotalCost:         breakdown.TotalCost,
	}
}

// publishKafkaSpendCopy projects the already-finalized SpendLog entry
// into Kafka exactly once. Kafka remains best-effort and independent from the
// authoritative spend writer; a failed enqueue is recorded on the same
// Postgres row's metadata before that row is queued or committed.
func (p *Proxy) publishKafkaSpendCopy(logCtx *RequestLogContext, entry *litellmdb.SpendLogEntry) error {
	if logCtx == nil || entry == nil || p.kafkaLog == nil || !p.kafkaLog.IsEnabled() || logCtx.kafkaSpendAttempted {
		return nil
	}
	logCtx.kafkaSpendAttempted = true

	usage, tokenCosts := parseShadowKafkaMetadata(entry)
	credName := logCtx.Credential.Name
	if logCtx.ActualCredentialName != "" {
		credName = logCtx.ActualCredentialName
	}
	event := p.buildKafkaSpendEvent(
		logCtx,
		credName,
		entry.ModelID,
		entry.APIKey,
		entry.UserID,
		entry.TeamID,
		entry.OrganizationID,
		entry.EndUser,
		entry.APIBase,
		entry.Status,
		entry.Spend,
		tokenCosts,
		0,
		entry.EndTime,
	)
	event.RequestID = entry.RequestID
	event.StartTime = entry.StartTime
	event.EndTime = entry.EndTime
	event.CompletionStartTime = entry.CompletionStartTime
	event.DurationMs = int64(entry.RequestDurationMS)
	if entry.CompletionStartTime != nil {
		ttft := entry.CompletionStartTime.Sub(entry.StartTime).Milliseconds()
		event.TTFTMs = &ttft
	} else {
		event.TTFTMs = nil
	}
	event.CallType = entry.CallType
	event.APIBase = entry.APIBase
	event.Status = entry.Status
	event.Model = entry.Model
	event.ModelID = entry.ModelID
	event.ModelGroup = entry.ModelGroup
	event.CredentialName = credName
	event.PromptTokens = usage.PromptTokens
	event.CompletionTokens = usage.CompletionTokens
	event.TotalTokens = entry.TotalTokens
	event.AudioInputTokens = usage.AudioInputTokens
	event.AudioOutputTokens = usage.AudioOutputTokens
	event.CachedInputTokens = usage.CachedInputTokens
	event.CacheCreationTokens = usage.CacheCreationTokens
	event.CachedOutputTokens = usage.CachedOutputTokens
	event.ReasoningTokens = usage.ReasoningTokens
	event.AcceptedPredictionTokens = usage.AcceptedPredictionTokens
	event.RejectedPredictionTokens = usage.RejectedPredictionTokens
	event.ImageCount = usage.ImageCount
	event.ImageTokens = usage.ImageTokens
	event.OutputImageTokens = usage.OutputImageTokens
	event.APIKeyHash = entry.APIKey
	event.UserID = entry.UserID
	event.TeamID = entry.TeamID
	event.OrganizationID = entry.OrganizationID
	event.EndUser = entry.EndUser
	event.RequesterIP = entry.RequesterIP
	event.SessionID = entry.SessionID
	if tokenCosts != nil {
		event.ImageCost = tokenCosts.ImageCost + tokenCosts.InputImageCost + tokenCosts.OutputImageCost
	}

	if err := p.kafkaLog.LogSpend(event); err != nil {
		p.logger.WarnContext(logCtx.Context(), "Failed to queue Kafka spend event",
			"error", err,
			"request_id", entry.RequestID,
		)
		reason := "publish_error"
		if errors.Is(err, kafkalog.ErrQueueFull) {
			reason = "queue_full"
		}
		if annotationErr := annotateKafkaFallback(entry, reason); annotationErr != nil {
			p.logger.WarnContext(logCtx.Context(), "Failed to annotate Kafka fallback on spend entry",
				"error", annotationErr,
				"request_id", entry.RequestID,
			)
		}
		return err
	}
	return nil
}

func annotateKafkaFallback(entry *litellmdb.SpendLogEntry, reason string) error {
	if entry == nil {
		return nil
	}
	metadata := make(map[string]any)
	if entry.Metadata != "" {
		if err := json.Unmarshal([]byte(entry.Metadata), &metadata); err != nil {
			return err
		}
	}
	metadata["kafka_fallback"] = true
	metadata["kafka_fallback_reason"] = reason
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	entry.Metadata = string(encoded)
	return nil
}

// buildKafkaSpendEvent maps a RequestLogContext plus the values already
// computed by logSpendToLiteLLMDB (cost, tokenCosts, end time, ...) onto the
// flat kafkalog.SpendEvent schema. Credential/server metadata is broken out
// into typed fields here instead of being folded into one JSON blob, as
// decided in the ТЗ (section 4) so ClickHouse can query it directly.
func (p *Proxy) buildKafkaSpendEvent(
	logCtx *RequestLogContext,
	credName, modelIDFormatted, hashedToken string,
	userID, teamID, organizationID, endUser, apiBase, status string,
	cost float64,
	tokenCosts *converter.TokenCosts,
	overheadMs float64,
	endTime time.Time,
) *kafkalog.SpendEvent {
	usage := logCtx.TokenUsage
	if usage == nil {
		usage = &converter.TokenUsage{}
	}

	realModel := logCtx.RealModelID
	if realModel == "" {
		realModel = logCtx.ModelID
	}

	var completionStartTime *time.Time
	var ttftMs *int64
	if !logCtx.CompletionStartTime.IsZero() {
		cst := logCtx.CompletionStartTime
		completionStartTime = &cst
		ttft := cst.Sub(logCtx.StartTime).Milliseconds()
		ttftMs = &ttft
	}

	var keyAlias, userAlias, teamAlias string
	if logCtx.TokenInfo != nil {
		keyAlias = logCtx.TokenInfo.KeyAlias
		userAlias = logCtx.TokenInfo.UserAlias
		teamAlias = logCtx.TokenInfo.TeamAlias
	}

	event := &kafkalog.SpendEvent{
		RequestID:           logCtx.RequestID,
		StartTime:           logCtx.StartTime,
		EndTime:             endTime,
		CompletionStartTime: completionStartTime,
		DurationMs:          endTime.Sub(logCtx.StartTime).Milliseconds(),
		TTFTMs:              ttftMs,

		CallType:     logCtx.Request.URL.Path,
		APIBase:      apiBase,
		Status:       status,
		HTTPStatus:   logCtx.HTTPStatus,
		ErrorMessage: logCtx.ErrorMsg,

		Model:      logCtx.ModelID,
		RealModel:  realModel,
		ModelID:    modelIDFormatted,
		ModelGroup: logCtx.ModelID,

		CredentialName:                 logCtx.Credential.Name,
		CredentialType:                 string(logCtx.Credential.Type),
		CredentialBaseURL:              logCtx.Credential.BaseURL,
		CredentialIsProxyRequest:       logCtx.IsProxyRequest,
		CredentialActualCredentialName: logCtx.ActualCredentialName,

		ServerRouterID: p.routerID,
		ServerVersion:  p.version,
		ServerCommit:   p.commit,

		PromptTokens:             usage.PromptTokens,
		CompletionTokens:         usage.CompletionTokens,
		TotalTokens:              usage.Total(),
		AudioInputTokens:         usage.AudioInputTokens,
		AudioOutputTokens:        usage.AudioOutputTokens,
		CachedInputTokens:        usage.CachedInputTokens,
		CacheCreationTokens:      usage.CacheCreationTokens,
		CachedOutputTokens:       usage.CachedOutputTokens,
		ReasoningTokens:          usage.ReasoningTokens,
		AcceptedPredictionTokens: usage.AcceptedPredictionTokens,
		RejectedPredictionTokens: usage.RejectedPredictionTokens,
		ImageCount:               usage.ImageCount,
		ImageTokens:              usage.ImageTokens,
		OutputImageTokens:        usage.OutputImageTokens,

		TotalCost: cost,

		APIKeyHash:     hashedToken,
		UserID:         userID,
		TeamID:         teamID,
		OrganizationID: organizationID,
		EndUser:        endUser,
		KeyAlias:       keyAlias,
		UserAlias:      userAlias,
		TeamAlias:      teamAlias,

		RequesterIP: getClientIP(logCtx.Request),
		SessionID:   logCtx.SessionID,
		OverheadMs:  overheadMs,
	}

	if tokenCosts != nil {
		event.InputCost = tokenCosts.InputCost
		event.OutputCost = tokenCosts.OutputCost
		event.AudioInputCost = tokenCosts.AudioInputCost
		event.AudioOutputCost = tokenCosts.AudioOutputCost
		event.ReasoningCost = tokenCosts.ReasoningCost
		event.CachedInputCost = tokenCosts.CachedInputCost
		event.CacheCreationCost = tokenCosts.CacheCreationCost
		event.CachedOutputCost = tokenCosts.CachedOutputCost
		event.PredictionCost = tokenCosts.PredictionCost
		event.ImageCost = tokenCosts.ImageCost
	}

	if status == "failure" {
		event.ErrorClass = mapHTTPStatusToErrorClass(logCtx.HTTPStatus)
	}

	return event
}
