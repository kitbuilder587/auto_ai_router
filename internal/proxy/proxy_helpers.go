package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/converter"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb"
	aimodels "github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
)

// ErrResponseBodyTooLarge is returned when a response body exceeds the configured size limit.
var ErrResponseBodyTooLarge = errors.New("response body too large")

// isTimeoutError checks if an error is a timeout error
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	// Check for context deadline exceeded
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Check for net.Error timeout
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return false
}

// isClientDisconnectError checks if an error indicates the client disconnected
// (broken pipe, connection reset, context canceled). These are expected during
// normal operation and should be logged at lower severity.
func isClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "write: broken pipe") ||
		strings.Contains(msg, "connection reset by peer")
}

func (p *Proxy) recordAbortedRequest(credential, endpoint, model string) {
	if p == nil || p.metrics == nil {
		return
	}
	p.metrics.RecordAbortedRequest(credential, endpoint, model)
}

func metricModelID(fallback string, logCtx *RequestLogContext) string {
	if logCtx != nil && logCtx.ModelID != "" {
		return logCtx.ModelID
	}
	return fallback
}

func endpointFromLogContext(logCtx *RequestLogContext) string {
	if logCtx == nil {
		return ""
	}
	return endpointFromRequest(logCtx.Request)
}

func endpointFromRequest(r *http.Request) string {
	if r != nil && r.URL != nil {
		return r.URL.Path
	}
	return ""
}

// extractErrorMessage returns the raw error response body as a string
// The HTTP status code is captured separately in error_code
func extractErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	// Return raw body (truncate if too large)
	const maxLen = 512
	if len(body) > maxLen {
		return string(body[:maxLen]) + "..."
	}
	return string(body)
}

// mapHTTPStatusToErrorClass maps HTTP status codes to LiteLLM exception class names
// Reference: https://docs.litellm.ai/docs/exception_mapping
func mapHTTPStatusToErrorClass(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest:
		return "BadRequestError"
	case http.StatusUnauthorized:
		return "AuthenticationError"
	case http.StatusForbidden:
		return "PermissionDeniedError"
	case http.StatusNotFound:
		return "NotFoundError"
	case http.StatusRequestTimeout:
		return "Timeout"
	case http.StatusUnprocessableEntity:
		return "UnprocessableEntityError"
	case http.StatusTooManyRequests:
		return "RateLimitError"
	case http.StatusServiceUnavailable:
		return "ServiceUnavailableError"
	case http.StatusInternalServerError:
		return "InternalServerError"
	default:
		if statusCode >= 400 && statusCode < 500 {
			return "BadRequestError"
		} else if statusCode >= 500 {
			return "APIConnectionError"
		}
		return "APIError"
	}
}

// buildMetadata builds metadata JSON with user/team alias, usage, cost, and optional error info
func buildMetadata(hashedToken string, tokenInfo *litellmdb.TokenInfo, errorMsg string, httpStatus int, usage *converter.TokenUsage, requesterIP string, costs *converter.TokenCosts, modelID string, overheadMs float64, kafkaFallbackReason string) string {
	var userID, teamID, organizationID string
	if tokenInfo != nil {
		userID = tokenInfo.UserID
		teamID = tokenInfo.TeamID
		organizationID = tokenInfo.OrganizationID
	}

	// Build usage_object and additional_usage_values
	promptTokensDetails := map[string]interface{}{
		"text_tokens":           nil,
		"audio_tokens":          0,
		"image_tokens":          nil,
		"cached_tokens":         0,
		"cache_creation_tokens": 0,
	}
	completionTokensDetails := map[string]interface{}{
		"text_tokens":                nil,
		"audio_tokens":               0,
		"image_tokens":               nil,
		"reasoning_tokens":           0,
		"accepted_prediction_tokens": 0,
		"rejected_prediction_tokens": 0,
	}

	var usageObject interface{}
	if usage != nil {
		promptTokensDetails["audio_tokens"] = usage.AudioInputTokens
		promptTokensDetails["image_tokens"] = usage.ImageTokens
		promptTokensDetails["cached_tokens"] = usage.CachedInputTokens
		promptTokensDetails["cache_creation_tokens"] = usage.CacheCreationTokens
		completionTokensDetails["audio_tokens"] = usage.AudioOutputTokens
		completionTokensDetails["image_tokens"] = usage.OutputImageTokens
		completionTokensDetails["reasoning_tokens"] = usage.ReasoningTokens
		completionTokensDetails["accepted_prediction_tokens"] = usage.AcceptedPredictionTokens
		completionTokensDetails["rejected_prediction_tokens"] = usage.RejectedPredictionTokens
		usageObject = map[string]interface{}{
			"total_tokens":              usage.Total(),
			"prompt_tokens":             usage.PromptTokens,
			"completion_tokens":         usage.CompletionTokens,
			"prompt_tokens_details":     promptTokensDetails,
			"completion_tokens_details": completionTokensDetails,
		}
	}

	additionalUsage := map[string]interface{}{
		"prompt_tokens_details": map[string]interface{}{
			"audio_tokens":          promptTokensDetails["audio_tokens"],
			"image_tokens":          promptTokensDetails["image_tokens"],
			"cached_tokens":         promptTokensDetails["cached_tokens"],
			"cache_creation_tokens": promptTokensDetails["cache_creation_tokens"],
		},
		"completion_tokens_details": map[string]interface{}{
			"audio_tokens":               completionTokensDetails["audio_tokens"],
			"image_tokens":               completionTokensDetails["image_tokens"],
			"reasoning_tokens":           completionTokensDetails["reasoning_tokens"],
			"accepted_prediction_tokens": completionTokensDetails["accepted_prediction_tokens"],
			"rejected_prediction_tokens": completionTokensDetails["rejected_prediction_tokens"],
		},
	}

	// Build cost_breakdown
	var costBreakdown interface{}
	if costs != nil {
		costBreakdown = map[string]interface{}{
			"input_cost":          costs.InputCost,
			"output_cost":         costs.OutputCost,
			"cached_input_cost":   costs.CachedInputCost,
			"cache_creation_cost": costs.CacheCreationCost,
			"total_cost":          costs.TotalCost,
			"original_cost":       costs.TotalCost,
			"margin_percent":      0.0,
			"discount_amount":     0.0,
			"tool_usage_cost":     0.0,
			"discount_percent":    0.0,
			"margin_fixed_amount": 0.0,
			"margin_total_amount": 0.0,
		}
	}

	metadata := map[string]interface{}{
		"batch_models":                  nil,
		"usage_object":                  usageObject,
		"user_api_key":                  hashedToken,
		"cost_breakdown":                costBreakdown,
		"applied_guardrails":            []interface{}{},
		"user_api_key_org_id":           organizationID,
		"requester_ip_address":          requesterIP,
		"user_api_key_team_id":          teamID,
		"user_api_key_user_id":          userID,
		"guardrail_information":         nil,
		"model_map_information":         map[string]interface{}{"model_map_key": modelID, "model_map_value": nil},
		"mcp_tool_call_metadata":        nil,
		"additional_usage_values":       additionalUsage,
		"cold_storage_object_key":       nil,
		"litellm_overhead_time_ms":      overheadMs,
		"vector_store_request_metadata": nil,
		"status":                        "success",
	}

	if tokenInfo != nil {
		if tokenInfo.KeyAlias != "" {
			metadata["user_api_key_alias"] = tokenInfo.KeyAlias
		}
		if tokenInfo.UserAlias != "" {
			metadata["user_api_key_user_alias"] = tokenInfo.UserAlias
		}
		if tokenInfo.TeamAlias != "" {
			metadata["user_api_key_team_alias"] = tokenInfo.TeamAlias
		}
	}

	if errorMsg != "" {
		metadata["error_information"] = map[string]interface{}{
			"error_message": errorMsg,
			"error_code":    httpStatus,
			"error_class":   mapHTTPStatusToErrorClass(httpStatus),
		}
		metadata["status"] = "failure"
	}

	// kafkaFallbackReason is set by the caller when publishing this event's
	// Kafka copy failed (e.g. queue full after the 5s backpressure wait, see
	// kafkalog.ErrQueueFull). Flagging it here — in the row that's about to be
	// inserted anyway — lets it be found later via metadata->>'kafka_fallback'
	// (e.g. by a DBA script) and re-published to Kafka, instead of the event
	// being lost entirely when Kafka is degraded. AIR intentionally does not
	// run its own resend job — see internal/litellmdb.Manager.MarkSpendLogKafkaFallback.
	if kafkaFallbackReason != "" {
		metadata["kafka_fallback"] = true
		metadata["kafka_fallback_reason"] = kafkaFallbackReason
	}

	jsonBytes, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Sprintf(`{"user_api_key":"%s","user_api_key_org_id":"%s","user_api_key_team_id":"%s","user_api_key_user_id":"%s"}`, hashedToken, organizationID, teamID, userID)
	}
	return string(jsonBytes)
}

type shadowMetadataInput struct {
	Identity           shadowcontext.Identity
	ContextState       shadowcontext.State
	ComparisonEligible bool
	Status             string
	ErrorMessage       string
	HTTPStatus         int
	Usage              *converter.TokenUsage
	UsageSource        string
	Costs              *converter.TokenCosts
	RequesterIP        string
	Billing            BillingContext
	OverheadMS         float64
	PriceStatus        string
	CostStatus         string
	PriceModel         string
	Price              *aimodels.ModelPrice
	PriceUpdatedAt     time.Time
	MaxRetries         int
	ActualCredential   string
	Outcome            string
	RequestMetadata    map[string]any
}

// shadowMetadataReservedKeys are generated from authenticated identity,
// measured usage/cost, request correlation, or AIR routing state. Client
// metadata may add any other top-level key but cannot replace these values.
var shadowMetadataReservedKeys = map[string]struct{}{
	"additional_headers":            {},
	"additional_usage_values":       {},
	"api_base":                      {},
	"applied_guardrails":            {},
	"attempted_retries":             {},
	"batch_models":                  {},
	"cache_key":                     {},
	"cold_storage_object_key":       {},
	"cost_breakdown":                {},
	"credential_name":               {},
	"custom_llm_provider":           {},
	"deployment":                    {},
	"error_information":             {},
	"guardrail_information":         {},
	"max_retries":                   {},
	"mcp_tool_call_metadata":        {},
	"model_group":                   {},
	"model_id":                      {},
	"model_info":                    {},
	"model_map_information":         {},
	"request_tags":                  {},
	"requester_ip_address":          {},
	"response_cost":                 {},
	"spend_logs_metadata":           {},
	"status":                        {},
	"tags":                          {},
	"team_alias":                    {},
	"team_id":                       {},
	"usage_object":                  {},
	"user_api_key":                  {},
	"vector_store_request_metadata": {},
}

// StandardLoggingMetadata reserves the user_api_key_* identity/budget family,
// while LiteLLM uses litellm_* for correlation and router-owned values. Treat
// both namespaces as closed so new upstream fields cannot silently become a
// spoofing bypass when LiteLLM adds them. Exact routing/cost keys that do not
// share one of those prefixes remain listed above; ordinary user metadata is
// preserved.
var shadowMetadataReservedPrefixes = []string{"user_api_key_", "litellm_"}

func isShadowMetadataReservedKey(key string) bool {
	if _, reserved := shadowMetadataReservedKeys[key]; reserved {
		return true
	}
	for _, prefix := range shadowMetadataReservedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func buildShadowMetadata(input shadowMetadataInput) string {
	usage := input.Usage
	if usage == nil {
		usage = &converter.TokenUsage{}
	}
	usageObject := map[string]interface{}{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.Total(),
		"prompt_tokens_details": map[string]interface{}{
			"audio_tokens":          usage.AudioInputTokens,
			"image_tokens":          usage.ImageTokens,
			"cached_tokens":         usage.CachedInputTokens,
			"cache_creation_tokens": usage.CacheCreationTokens,
		},
		"completion_tokens_details": map[string]interface{}{
			"audio_tokens":               usage.AudioOutputTokens,
			"cached_tokens":              usage.CachedOutputTokens,
			"image_tokens":               usage.OutputImageTokens,
			"reasoning_tokens":           usage.ReasoningTokens,
			"accepted_prediction_tokens": usage.AcceptedPredictionTokens,
			"rejected_prediction_tokens": usage.RejectedPredictionTokens,
		},
	}
	additionalUsageValues := map[string]interface{}{
		"prompt_tokens_details":       usageObject["prompt_tokens_details"],
		"completion_tokens_details":   usageObject["completion_tokens_details"],
		"cache_creation_input_tokens": usage.CacheCreationTokens,
		"cache_read_input_tokens":     usage.CachedInputTokens,
	}

	var costBreakdown interface{}
	if input.Costs != nil {
		inputCost := input.Costs.InputCost + input.Costs.AudioInputCost + input.Costs.InputImageCost
		outputCost := input.Costs.OutputCost + input.Costs.AudioOutputCost +
			input.Costs.ReasoningCost + input.Costs.CachedOutputCost +
			input.Costs.PredictionCost + input.Costs.OutputImageCost
		costBreakdown = map[string]interface{}{
			"input_cost":          inputCost,
			"cache_read_cost":     input.Costs.CachedInputCost,
			"cache_creation_cost": input.Costs.CacheCreationCost,
			"output_cost":         outputCost,
			"reasoning_cost":      input.Costs.ReasoningCost,
			"audio_input_cost":    input.Costs.AudioInputCost,
			"audio_output_cost":   input.Costs.AudioOutputCost,
			"prediction_cost":     input.Costs.PredictionCost,
			"input_image_cost":    input.Costs.InputImageCost,
			"output_image_cost":   input.Costs.OutputImageCost,
			"image_cost":          input.Costs.ImageCost,
			"total_cost":          input.Costs.TotalCost,
		}
	}

	var priceSnapshot interface{}
	if input.Price != nil {
		priceSnapshot = map[string]interface{}{
			"model":                            input.PriceModel,
			"registry_updated_at":              input.PriceUpdatedAt.UTC().Format(time.RFC3339Nano),
			"input_cost_per_token":             input.Price.InputCostPerToken,
			"output_cost_per_token":            input.Price.OutputCostPerToken,
			"input_cost_per_audio_token":       input.Price.InputCostPerAudioToken,
			"output_cost_per_audio_token":      input.Price.OutputCostPerAudioToken,
			"input_cost_per_image_token":       input.Price.InputCostPerImageToken,
			"output_cost_per_image_token":      input.Price.OutputCostPerImageToken,
			"output_cost_per_reasoning_token":  input.Price.OutputCostPerReasoningToken,
			"cache_read_cost_per_token":        firstNonZero(input.Price.InputCostPerCachedToken, input.Price.CacheReadInputTokenCost),
			"cache_write_cost_per_token":       input.Price.CacheCreationInputTokenCost,
			"output_cost_per_prediction_token": input.Price.OutputCostPerPredictionToken,
			"output_cost_per_image":            input.Price.OutputCostPerImage,
		}
	}

	attempts := input.Billing.Attempts()
	attemptedRetries := len(attempts) - 1
	if attemptedRetries < 0 {
		attemptedRetries = 0
	}
	extension := map[string]interface{}{
		"comparison_eligible":  input.ComparisonEligible,
		"shadow_context_state": string(input.ContextState),
		"actual_provider":      input.Billing.Provider(),
		"actual_credential":    input.ActualCredential,
		"actual_upstream_host": input.Billing.TargetHost(),
		"price_status":         input.PriceStatus,
		"cost_status":          input.CostStatus,
		"price_snapshot":       priceSnapshot,
		"usage_source":         input.UsageSource,
		"attempts":             attempts,
		"air_event_id":         input.Billing.EventID(),
		"provider_response_id": input.Billing.ProviderResponseID(),
		"outcome":              input.Outcome,
		"original_call_type":   string(input.Billing.CallType()),
	}
	metadata := map[string]interface{}{
		"batch_models":                  nil,
		"usage_object":                  usageObject,
		"user_api_key":                  input.Identity.APIKeyHash,
		"cost_breakdown":                costBreakdown,
		"applied_guardrails":            []interface{}{},
		"user_api_key_org_id":           input.Identity.OrganizationID,
		"user_api_key_project_id":       input.Identity.ProjectID,
		"user_api_key_agent_id":         input.Identity.AgentID,
		"requester_ip_address":          input.RequesterIP,
		"user_api_key_team_id":          input.Identity.TeamID,
		"user_api_key_user_id":          input.Identity.UserID,
		"litellm_call_id":               input.Billing.CallID(),
		"attempted_retries":             attemptedRetries,
		"max_retries":                   input.MaxRetries,
		"guardrail_information":         nil,
		"model_map_information":         map[string]interface{}{"model_map_key": input.Billing.BackendModel(), "model_map_value": nil},
		"mcp_tool_call_metadata":        nil,
		"additional_usage_values":       additionalUsageValues,
		"cold_storage_object_key":       nil,
		"litellm_overhead_time_ms":      input.OverheadMS,
		"vector_store_request_metadata": nil,
		"status":                        input.Status,
		"spend_logs_metadata":           extension,
	}
	for key, value := range input.RequestMetadata {
		if isShadowMetadataReservedKey(key) {
			continue
		}
		metadata[key] = value
	}
	if input.ErrorMessage != "" {
		metadata["error_information"] = map[string]interface{}{
			"error_message": input.ErrorMessage,
			"error_code":    input.HTTPStatus,
			"error_class":   mapHTTPStatusToErrorClass(input.HTTPStatus),
		}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return `{"spend_logs_metadata":{"comparison_eligible":false,"metadata_error":true}}`
	}
	return string(encoded)
}

func firstNonZero(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

// extractEndUser extracts end_user from request headers or body
func extractEndUser(r *http.Request) string {
	// Check X-End-User header first
	if endUser := r.Header.Get("X-End-User"); endUser != "" {
		return endUser
	}
	return ""
}

// getClientIP gets the client IP address
func getClientIP(r *http.Request) string {
	// X-Forwarded-For header (first IP)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	// X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	// RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// extractVersionSuffix returns the version segment (e.g. "/v1", "/v4") from the
// end of a URL base path, or empty string if none found. Only matches /v followed
// by one or more digits at the very end.
func extractVersionSuffix(baseURL string) string {
	idx := strings.LastIndex(baseURL, "/")
	if idx < 0 {
		return ""
	}
	segment := baseURL[idx:] // e.g. "/v1"
	if len(segment) < 3 || segment[1] != 'v' {
		return ""
	}
	for _, c := range segment[2:] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return segment
}

// extractVersionPrefix returns the version segment (e.g. "/v1") from the
// beginning of a URL path, or empty string if none found.
func extractVersionPrefix(urlPath string) string {
	if len(urlPath) < 3 || urlPath[0] != '/' || urlPath[1] != 'v' {
		return ""
	}
	i := 2
	for i < len(urlPath) && urlPath[i] >= '0' && urlPath[i] <= '9' {
		i++
	}
	if i == 2 {
		return "" // no digits after /v
	}
	// Must end at string end or next slash
	if i < len(urlPath) && urlPath[i] != '/' {
		return ""
	}
	return urlPath[:i]
}
