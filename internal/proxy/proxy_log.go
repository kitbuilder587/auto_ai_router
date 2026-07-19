package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/converter"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb"
	"github.com/mixaill76/auto_ai_router/internal/logger"
	aimodels "github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/monitoring"
	"github.com/mixaill76/auto_ai_router/internal/security"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/mixaill76/auto_ai_router/internal/utils"
)

// ==================== Unified error logging ====================

// logUpstreamError emits the unified ERROR record for a failed upstream request.
// Every final provider failure must go through this helper so that a single ERROR
// line contains everything needed for debugging:
// error_code, credential (+provider), model, response_body.
// extra accepts additional context pairs (request_id, url, error, ...).
func (p *Proxy) logUpstreamError(ctx context.Context, msg string, errorCode int, cred *config.CredentialConfig, modelID string, responseBody []byte, extra ...any) {
	args := make([]any, 0, 10+len(extra))
	args = append(args, "error_code", errorCode)
	if cred != nil {
		args = append(args, "credential", cred.Name, "provider", string(cred.Type))
	}
	args = append(args, "model", modelID)
	if len(responseBody) > 0 {
		args = appendResponseBodyForLogs(args, cred, string(responseBody))
	}
	args = append(args, extra...)
	p.logger.ErrorContext(ctx, msg, args...)
}

func appendResponseBodyForLogs(args []any, cred *config.CredentialConfig, body string) []any {
	if shouldMaskUpstreamErrors(cred) {
		return append(args,
			"response_body_masked", true,
			"response_body", body,
		)
	}
	return append(args, "response_body", logger.TruncateLongFields(body, 500))
}

func shouldMaskUpstreamErrors(cred *config.CredentialConfig) bool {
	return isCometAPICredential(cred)
}

func isCometAPICredential(cred *config.CredentialConfig) bool {
	if cred == nil {
		return false
	}
	if cred.Type == config.ProviderTypeCometAPI {
		return true
	}
	name := strings.ToLower(cred.Name)
	return isCometAPIHost(cred.BaseURL) ||
		strings.Contains(name, "cometapi") ||
		strings.Contains(name, "comet-api")
}

func isCometAPIHost(rawBaseURL string) bool {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		u, err = url.Parse("https://" + baseURL)
		if err != nil {
			return false
		}
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return host == "cometapi.com" || strings.HasSuffix(host, ".cometapi.com")
}

// logStreamHandlerError logs a streaming handler failure. Client disconnects are
// expected during normal operation and go to DEBUG; real failures go to ERROR.
func (p *Proxy) logStreamHandlerError(ctx context.Context, msg string, err error, extra ...any) {
	args := append([]any{"error", err}, extra...)
	if isClientDisconnectError(err) {
		p.logger.DebugContext(ctx, msg+" (client disconnected)", args...)
		return
	}
	p.logger.ErrorContext(ctx, msg, args...)
}

// logTransformedResponse logs a transformed response at debug level
func (p *Proxy) logTransformedResponse(ctx context.Context, credName, providerName string, body []byte) {
	if p.logger.Enabled(ctx, slog.LevelDebug) {
		p.logger.DebugContext(ctx, "Transformed response to OpenAI format",
			"credential", credName,
			"provider", providerName,
			"body", logger.TruncateLongFields(string(body), 500),
		)
	}
}

// ==================== LiteLLM DB Integration ====================
// handleLiteLLMAuthError handles LiteLLM authentication errors
// Returns true if error was handled and response was written
func (p *Proxy) handleLiteLLMAuthError(ctx context.Context, w http.ResponseWriter, err error, token string) bool {
	// Map error types to HTTP status and message
	errorMap := map[error]struct {
		status  int
		message string
		logMsg  string
	}{
		litellmdb.ErrTokenNotFound:  {http.StatusUnauthorized, "Invalid token", "Token not found"},
		litellmdb.ErrTokenBlocked:   {http.StatusForbidden, "Token blocked", "Token blocked"},
		litellmdb.ErrTeamBlocked:    {http.StatusForbidden, "Team blocked", "Team blocked"},
		litellmdb.ErrProjectBlocked: {http.StatusForbidden, "Project blocked", "Project blocked"},
		litellmdb.ErrTokenExpired:   {http.StatusUnauthorized, "Token expired", "Token expired"},
		litellmdb.ErrBudgetExceeded: {http.StatusPaymentRequired, "Budget exceeded", "Budget exceeded"},
	}

	// Check for connection failure first (requires fallback, not an error response)
	if errors.Is(err, litellmdb.ErrConnectionFailed) {
		return false
	}

	// Check for known auth errors — client-side issues (bad/blocked/expired token,
	// budget), not service failures, so they are logged at WARN.
	for errType, info := range errorMap {
		if errors.Is(err, errType) {
			p.logger.WarnContext(ctx, info.logMsg,
				"error_code", info.status,
				"token_prefix", security.MaskAPIKey(token))
			switch info.status {
			case http.StatusForbidden:
				WriteErrorForbidden(w, info.message)
			case http.StatusPaymentRequired:
				WriteErrorPaymentRequired(w, info.message)
			default:
				WriteErrorUnauthorized(w, info.message)
			}
			return true
		}
	}

	// Unknown error — unexpected server-side failure, keep at ERROR
	p.logger.ErrorContext(ctx, "Auth error",
		"error_code", http.StatusInternalServerError,
		"error", err,
		"token_prefix", security.MaskAPIKey(token))
	WriteErrorInternal(w, "Internal Server Error")
	return true
}

func (p *Proxy) finalizeDeferredShadowSpend(logCtx *RequestLogContext) error {
	if logCtx == nil {
		return nil
	}
	entry := logCtx.pendingSpendEntry
	if entry == nil {
		entry = p.buildShadowSpendEntry(logCtx)
		logCtx.pendingSpendEntry = entry
	}
	_ = p.publishKafkaSpendCopy(logCtx, entry)
	if err := p.queueShadowSpendEntry(entry); err != nil {
		return err
	}
	logCtx.pendingSpendEntry = nil
	logCtx.Logged = true
	return nil
}

func (p *Proxy) queueShadowSpendEntry(entry *litellmdb.SpendLogEntry) error {
	if entry == nil || p.spendLogger == nil || !p.spendLogger.IsEnabled() {
		return nil
	}
	return p.spendLogger.LogSpend(entry)
}

// setCommittedKeySpendSnapshot installs the latest committed key spend before
// response headers can be written. It intentionally ignores the auth cache:
// cached TokenInfo may be stale or may describe AIR's master key on a signed
// LiteLLM -> AIR request.
const shadowSpendResponseBudget = 2 * time.Second

func boundedShadowSpendContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, shadowSpendResponseBudget)
}

func (p *Proxy) setCommittedKeySpendSnapshot(ctx context.Context, headers http.Header, logCtx *RequestLogContext) {
	if logCtx != nil {
		logCtx.keySpendSnapshot = 0
		logCtx.keySpendSnapshotKnown = false
	}
	apiKeyHash := spendIdentityKey(logCtx)
	if apiKeyHash == "" || p.spendLogger == nil || !p.spendLogger.IsEnabled() {
		setLiteLLMKeySpendHeaderForRequest(headers, 0, false, logCtx)
		return
	}

	dbCtx, cancel := boundedShadowSpendContext(ctx)
	defer cancel()
	spend, known, err := p.spendLogger.ReadKeySpend(dbCtx, apiKeyHash)
	if err != nil {
		setLiteLLMKeySpendHeaderForRequest(headers, 0, false, logCtx)
		p.logger.WarnContext(ctx, "Failed to read committed key spend",
			"error", err,
			"request_id", logCtx.RequestID,
		)
		return
	}
	if logCtx != nil {
		// This is a request-local PostgreSQL statement snapshot, captured only
		// after authentication resolved the tenant identity. It is deliberately
		// not sourced from TokenInfo or a process-local cache.
		logCtx.keySpendSnapshot = spend
		logCtx.keySpendSnapshotKnown = known
	}
	setLiteLLMKeySpendHeaderForRequest(headers, spend, known, logCtx)
}

func setKeySpendHeaderFromSnapshot(headers http.Header, logCtx *RequestLogContext) {
	if logCtx == nil {
		setLiteLLMKeySpendHeaderForRequest(headers, 0, false, nil)
		return
	}
	setLiteLLMKeySpendHeaderForRequest(
		headers,
		logCtx.keySpendSnapshot,
		logCtx.keySpendSnapshotKnown,
		logCtx,
	)
}

type shadowSpendCommitDisposition string

const (
	shadowSpendCommitSkipped       shadowSpendCommitDisposition = "skipped"
	shadowSpendCommitted           shadowSpendCommitDisposition = "committed"
	shadowSpendReplayQueued        shadowSpendCommitDisposition = "replay_queued"
	shadowSpendReplayEnqueueFailed shadowSpendCommitDisposition = "replay_enqueue_failed"
)

type shadowSpendCommitResult struct {
	Disposition shadowSpendCommitDisposition
}

// commitShadowSpendBeforeResponse synchronously commits one non-stream spend
// effect and publishes the inclusive key total only after PostgreSQL has
// acknowledged the transaction. If the logger retains an ambiguous attempt for
// exact idempotent replay, only a known pre-request PostgreSQL snapshot may be
// reused; unclassified hard failures omit the header.
func (p *Proxy) commitShadowSpendBeforeResponse(ctx context.Context, headers http.Header, logCtx *RequestLogContext) (shadowSpendCommitResult, error) {
	entry := p.buildShadowSpendEntry(logCtx)
	if entry != nil {
		setLiteLLMResponseCostHeaderForRequest(headers, entry.Spend, logCtx)
		_ = p.publishKafkaSpendCopy(logCtx, entry)
	}
	if entry == nil || p.spendLogger == nil || !p.spendLogger.IsEnabled() {
		if logCtx != nil {
			logCtx.Logged = true
		}
		return shadowSpendCommitResult{Disposition: shadowSpendCommitSkipped}, nil
	}

	dbCtx, cancel := boundedShadowSpendContext(ctx)
	defer cancel()
	result, err := p.spendLogger.CommitSpend(dbCtx, entry)
	if err != nil {
		if result.ReplayRetained {
			// The inclusive value is unknown because the synchronous transaction
			// did not receive a commit acknowledgement. The logger still owns the
			// exact idempotent event, so keep the earlier PostgreSQL statement
			// snapshot instead of fabricating an inclusive total or omitting a
			// known value. A later external commit may make this snapshot stale,
			// but it can never make it uncommitted or greater than the database
			// state that was observed when it was read.
			setKeySpendHeaderFromSnapshot(headers, logCtx)
			if logCtx != nil {
				logCtx.pendingSpendEntry = nil
				logCtx.Logged = true
			}
			return shadowSpendCommitResult{Disposition: shadowSpendReplayQueued}, err
		}
		// Without lifecycle-owned exact retention, do not allow an earlier
		// value to survive an unclassified hard failure.
		setLiteLLMKeySpendHeaderForRequest(headers, 0, false, logCtx)
		if logCtx != nil {
			logCtx.pendingSpendEntry = entry
		}
		queueErr := p.queueShadowSpendEntry(entry)
		if queueErr == nil && logCtx != nil {
			logCtx.pendingSpendEntry = nil
			logCtx.Logged = true
		}
		if queueErr != nil {
			return shadowSpendCommitResult{Disposition: shadowSpendReplayEnqueueFailed}, errors.Join(err, queueErr)
		}
		return shadowSpendCommitResult{Disposition: shadowSpendReplayQueued}, err
	}

	if logCtx != nil {
		// A successful commit always replaces the pre-request snapshot with the
		// inclusive scalar observed under the transaction's row lock.
		logCtx.keySpendSnapshot = result.KeySpend
		logCtx.keySpendSnapshotKnown = result.KeySpendKnown
	}
	setLiteLLMKeySpendHeaderForRequest(headers, result.KeySpend, result.KeySpendKnown, logCtx)
	if logCtx != nil {
		logCtx.pendingSpendEntry = nil
		logCtx.Logged = true
	}
	return shadowSpendCommitResult{Disposition: shadowSpendCommitted}, nil
}

func (p *Proxy) buildShadowSpendEntry(logCtx *RequestLogContext) *litellmdb.SpendLogEntry {
	if logCtx == nil || logCtx.Credential == nil || logCtx.Request == nil {
		return nil
	}

	endTime := utils.NowUTC()
	status := logCtx.Status
	if status == "" || status == "unknown" {
		if logCtx.HTTPStatus >= http.StatusBadRequest {
			status = "failure"
		} else {
			status = "success"
		}
	}
	usage := logCtx.TokenUsage
	usageSource := logCtx.UsageSource
	if usage == nil {
		usage = &converter.TokenUsage{}
		usageSource = "missing"
	} else {
		usageCopy := *usage
		usage = &usageCopy
		if usageSource == "" {
			usageSource = "provider"
		}
	}
	if logCtx.IsImageGeneration {
		// OpenAI image usage reports generated image tokens in output_tokens.
		// Price those tokens with the image-output rate even when the optional
		// completion_tokens_details.image_tokens breakdown is absent.
		if usage.OutputImageTokens == 0 && usage.CompletionTokens > 0 {
			usage.OutputImageTokens = usage.CompletionTokens
		}
		if usage.ImageCount == 0 && logCtx.ImageCount > 0 {
			usage.ImageCount = logCtx.ImageCount
		}
		if usage.Total() == 0 && usage.ImageCount > 0 {
			usageSource = "request_parameters"
		}
	}
	billing := logCtx.Billing
	if billing.EventID() == "" {
		billing = NewBillingContext(logCtx.RequestID, logCtx.CallID, logCtx.Request.URL.Path, logCtx.ShadowContext.Identity).
			WithPublicModel(logCtx.PublicModelID).
			WithRouting(logCtx.ModelID, logCtx.RealModelID, string(logCtx.Credential.Type), logCtx.Credential.Name, logCtx.TargetURL)
	}
	backendModel := billing.BackendModel()
	if backendModel == "" {
		backendModel = logCtx.ModelID
	}
	priceModel := billing.ProviderModel()
	if priceModel == "" {
		priceModel = logCtx.RealModelID
	}
	if priceModel == "" {
		priceModel = backendModel
	}

	priceStatus := "missing_registry"
	var modelPrice *aimodels.ModelPrice
	var tokenCosts *converter.TokenCosts
	var priceUpdatedAt time.Time
	resolvedPriceModel := priceModel
	costStatus := "price_missing"
	if p.priceRegistry != nil {
		priceStatus = "missing_model"
		modelPrice = p.priceRegistry.GetPrice(priceModel)
		if modelPrice == nil && priceModel != backendModel {
			modelPrice = p.priceRegistry.GetPrice(backendModel)
			if modelPrice != nil {
				resolvedPriceModel = backendModel
			}
		}
		if modelPrice != nil {
			priceStatus = "found"
			priceUpdatedAt = p.priceRegistry.LastUpdate()
			costStatus = "calculated"
			if (usageSource == "missing" && status != "failure") ||
				(logCtx.IsImageGeneration && usage.Total() == 0 && modelPrice.OutputCostPerImage <= 0) {
				costStatus = "insufficient_usage"
			} else {
				tokenCosts = modelPrice.CalculateCosts(usage)
			}
		}
	}
	var cost float64
	if tokenCosts != nil {
		cost = tokenCosts.TotalCost
	}
	if priceStatus != "found" {
		p.logger.WarnContext(logCtx.Context(), "Shadow spend row has no price",
			"price_status", priceStatus,
			"model", priceModel,
		)
		// A successful, token-consuming request whose model has no price is
		// persisted with spend=0, which is indistinguishable in the `spend`
		// column from a legitimately free/cache-hit row. Surface it as an
		// explicit metric so a paid model without a price is never a silent zero.
		if status == "success" && usage.Total() > 0 {
			monitoring.ShadowSpendPriceMissingTotal.WithLabelValues(priceStatus).Inc()
		}
	} else if costStatus != "calculated" {
		p.logger.WarnContext(logCtx.Context(), "Shadow spend row cost cannot be calculated",
			"cost_status", costStatus,
			"usage_source", usageSource,
			"model", resolvedPriceModel,
		)
	}

	identity, trustedIdentity := resolveSpendIdentity(logCtx, billing)
	routingCredential := billing.Credential()
	if routingCredential == "" {
		routingCredential = logCtx.Credential.Name
	}
	// A valid signed LiteLLM identity already names the authoritative model-table
	// deployment. Direct AIR requests resolve it from the DB model snapshot using
	// the public model plus the credential that ultimately served this response.
	if logCtx.ShadowContext.State != shadowcontext.StateValid && p.modelManager != nil {
		if deploymentID, ok := p.modelManager.GetDeploymentID(identity.PublicModel, routingCredential); ok {
			identity.DeploymentID = deploymentID
		}
	}
	comparisonEligible := trustedIdentity &&
		priceStatus == "found" && identity.APIKeyHash != "" &&
		identity.PublicModel != "" && identity.DeploymentID != "" &&
		billing.CallType() != RouteUnknown && costStatus == "calculated"
	actualCredential := routingCredential
	if actualCredential == "" {
		actualCredential = logCtx.Credential.Name
	}
	if logCtx.ActualCredentialName != "" {
		actualCredential = logCtx.ActualCredentialName
	}
	requesterIP := getClientIP(logCtx.Request)
	metadata := buildShadowMetadata(shadowMetadataInput{
		Identity:           identity,
		ContextState:       logCtx.ShadowContext.State,
		ComparisonEligible: comparisonEligible,
		Status:             status,
		ErrorMessage:       logCtx.ErrorMsg,
		HTTPStatus:         logCtx.HTTPStatus,
		Usage:              usage,
		UsageSource:        usageSource,
		Costs:              tokenCosts,
		RequesterIP:        requesterIP,
		Billing:            billing,
		OverheadMS:         float64(endTime.Sub(logCtx.StartTime).Microseconds()) / 1000,
		PriceStatus:        priceStatus,
		CostStatus:         costStatus,
		PriceModel:         resolvedPriceModel,
		Price:              modelPrice,
		PriceUpdatedAt:     priceUpdatedAt,
		MaxRetries:         p.maxProviderRetries,
		ActualCredential:   actualCredential,
		Outcome:            logCtx.StreamOutcome,
		RequestMetadata:    logCtx.RequestMetadata,
	})
	tags, _ := json.Marshal(normalizeIdentityTags(identity.Tags))
	sessionID := logCtx.SessionID
	if sessionID == "" {
		sessionID = billing.EventID()
	}
	apiBase := p.spendAPIBase
	if apiBase == "" {
		apiBase = config.ShadowSpendAPIBase
	}
	var completionStartTime *time.Time
	if !logCtx.CompletionStartTime.IsZero() {
		value := logCtx.CompletionStartTime
		completionStartTime = &value
	}
	callType := spendLogCallType(status, billing, hasMaterialUsage(usage) || cost != 0)
	cacheHit := "None"
	if billing.CallType() == RouteCompletion || billing.CallType() == RouteTextCompletion {
		cacheHit = "False"
	}
	var toolKeyAlias string
	if logCtx.TokenInfo != nil {
		toolKeyAlias = logCtx.TokenInfo.KeyAlias
	}
	return &litellmdb.SpendLogEntry{
		RequestID:           billing.SpendRequestID(),
		AirEventID:          billing.EventID(),
		StartTime:           logCtx.StartTime,
		EndTime:             endTime,
		RequestDurationMS:   int(endTime.Sub(logCtx.StartTime).Milliseconds()),
		CompletionStartTime: completionStartTime,
		CallType:            callType,
		APIBase:             apiBase,
		Model:               backendModel,
		ModelID:             identity.DeploymentID,
		ModelGroup:          identity.PublicModel,
		CustomLLMProvider:   string(config.ProviderTypeOpenAI),
		PromptTokens:        usage.PromptTokens,
		CompletionTokens:    usage.CompletionTokens,
		TotalTokens:         usage.Total(),
		Metadata:            metadata,
		CacheHit:            cacheHit,
		CacheKey:            "Cache OFF",
		Spend:               cost,
		APIKey:              identity.APIKeyHash,
		UserID:              identity.UserID,
		TeamID:              identity.TeamID,
		OrganizationID:      identity.OrganizationID,
		ProjectID:           identity.ProjectID,
		EndUser:             identity.EndUser,
		RequesterIP:         requesterIP,
		Status:              status,
		SessionID:           sessionID,
		RequestTags:         string(tags),
		AgentID:             identity.AgentID,
		ComparisonEligible:  comparisonEligible,
		DeclaredToolNames:   append([]string(nil), logCtx.DeclaredToolNames...),
		ToolKeyAlias:        toolKeyAlias,
	}
}

// spendLogCallType keeps a canonical route whenever a terminal failure carries
// billable/token effects.  Zero-effect non-chat failures retain LiteLLM's
// historical empty raw call_type and are projected into daily tables from the
// signed original_call_type metadata.  This prevents partial stream usage from
// becoming an unrouteable aggregate while preserving the observed zero-effect
// failure shape.
func spendLogCallType(status string, billing BillingContext, hasEffect bool) string {
	if status == "failure" && billing.CallType() != RouteCompletion && !hasEffect {
		return ""
	}
	return string(billing.CallType())
}

func hasMaterialUsage(usage *converter.TokenUsage) bool {
	if usage == nil {
		return false
	}
	return usage.PromptTokens != 0 ||
		usage.CompletionTokens != 0 ||
		usage.AudioInputTokens != 0 ||
		usage.AudioOutputTokens != 0 ||
		usage.CachedInputTokens != 0 ||
		usage.CacheCreationTokens != 0 ||
		usage.CachedOutputTokens != 0 ||
		usage.ReasoningTokens != 0 ||
		usage.AcceptedPredictionTokens != 0 ||
		usage.RejectedPredictionTokens != 0 ||
		usage.ImageCount != 0 ||
		usage.ImageTokens != 0 ||
		usage.OutputImageTokens != 0
}

// resolveSpendIdentity selects exactly one authenticated identity source. A
// valid signed LiteLLM context is authoritative for the chained route. Direct
// AIR requests fall back to the TokenInfo loaded while authenticating the
// bearer token; unverified shadow claims are never merged into that identity.
func resolveSpendIdentity(logCtx *RequestLogContext, billing BillingContext) (shadowcontext.Identity, bool) {
	if logCtx == nil {
		return shadowcontext.Identity{Tags: []string{}}, false
	}
	if logCtx.ShadowContext.State == shadowcontext.StateValid {
		identity := logCtx.ShadowContext.Identity
		identity.Tags = normalizeIdentityTags(identity.Tags)
		return identity, true
	}
	if logCtx.TokenInfo == nil {
		return shadowcontext.Identity{Tags: []string{}}, false
	}

	apiKeyHash := spendIdentityKey(logCtx)
	endUser := extractEndUser(logCtx.Request)
	if endUser == "" {
		// Existing request parsing records the OpenAI user/safety identifier in
		// SessionID. It is the best authenticated-request fallback when no
		// explicit end-user header is present.
		endUser = logCtx.SessionID
	}
	return shadowcontext.Identity{
		APIKeyHash:     apiKeyHash,
		UserID:         logCtx.TokenInfo.UserID,
		TeamID:         logCtx.TokenInfo.TeamID,
		OrganizationID: logCtx.TokenInfo.OrganizationID,
		ProjectID:      logCtx.TokenInfo.ProjectID,
		AgentID:        logCtx.TokenInfo.AgentID,
		PublicModel:    billing.PublicModel(),
		DeploymentID:   billing.DeploymentID(),
		EndUser:        endUser,
		Tags:           mergeIdentityTags(logCtx.TokenInfo.Tags, logCtx.RequestTags),
		CallID:         billing.CallID(),
	}, true
}

// spendIdentityKey returns exactly the key whose counters receive this spend.
// A verified signed identity is authoritative even when empty; falling back to
// AIR's bearer/master key in that case would attribute chained tenant spend to
// the wrong key.
func spendIdentityKey(logCtx *RequestLogContext) string {
	if logCtx == nil {
		return ""
	}
	if logCtx.ShadowContext.State == shadowcontext.StateValid {
		return logCtx.ShadowContext.Identity.APIKeyHash
	}
	if logCtx.TokenInfo != nil && logCtx.TokenInfo.Token != "" {
		return logCtx.TokenInfo.Token
	}
	if logCtx.Token != "" {
		return litellmdb.HashToken(logCtx.Token)
	}
	return ""
}

func normalizeIdentityTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}
	return append([]string(nil), tags...)
}

// mergeIdentityTags matches LiteLLM's request attribution shape: authenticated
// key tags come first, request tags are appended, and exact duplicates are
// removed without reordering. Signed chained identity never calls this merge;
// its already-authenticated tag list remains authoritative.
func mergeIdentityTags(identityTags, requestTags []string) []string {
	result := make([]string, 0, len(identityTags)+len(requestTags))
	seen := make(map[string]struct{}, len(identityTags)+len(requestTags))
	for _, tags := range [][]string{identityTags, requestTags} {
		for _, tag := range tags {
			if tag == "" {
				continue
			}
			if _, exists := seen[tag]; exists {
				continue
			}
			seen[tag] = struct{}{}
			result = append(result, tag)
		}
	}
	return result
}
