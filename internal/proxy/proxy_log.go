package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/converter"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb"
	"github.com/mixaill76/auto_ai_router/internal/logger"
	"github.com/mixaill76/auto_ai_router/internal/security"
	"github.com/mixaill76/auto_ai_router/internal/utils"
)

// logTransformedResponse logs a transformed response at debug level
func (p *Proxy) logTransformedResponse(credName, providerName string, body []byte) {
	if p.logger.Enabled(context.Background(), slog.LevelDebug) {
		p.logger.Debug("Transformed response to OpenAI format",
			"credential", credName,
			"provider", providerName,
			"body", logger.TruncateLongFields(string(body), 500),
		)
	}
}

// ==================== LiteLLM DB Integration ====================
// handleLiteLLMAuthError handles LiteLLM authentication errors
// Returns true if error was handled and response was written
func (p *Proxy) handleLiteLLMAuthError(w http.ResponseWriter, err error, token string) bool {
	// Map error types to HTTP status and message
	errorMap := map[error]struct {
		status  int
		message string
		logMsg  string
	}{
		litellmdb.ErrTokenNotFound:  {http.StatusUnauthorized, "Invalid token", "Token not found"},
		litellmdb.ErrTokenBlocked:   {http.StatusForbidden, "Token blocked", "Token blocked"},
		litellmdb.ErrTokenExpired:   {http.StatusUnauthorized, "Token expired", "Token expired"},
		litellmdb.ErrBudgetExceeded: {http.StatusPaymentRequired, "Budget exceeded", "Budget exceeded"},
	}

	// Check for connection failure first (requires fallback, not an error response)
	if errors.Is(err, litellmdb.ErrConnectionFailed) {
		return false
	}

	// Check for known auth errors
	for errType, info := range errorMap {
		if errors.Is(err, errType) {
			p.logger.Error(info.logMsg, "token_prefix", security.MaskAPIKey(token))
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

	// Unknown error
	p.logger.Error("Auth error", "error", err, "token_prefix", security.MaskAPIKey(token))
	WriteErrorInternal(w, "Internal Server Error")
	return true
}

// logSpendToLiteLLMDB logs request to LiteLLM_SpendLogs table
// Returns error if the log entry cannot be queued (e.g., queue full)
func (p *Proxy) logSpendToLiteLLMDB(logCtx *RequestLogContext) error {
	if p.LiteLLMDB == nil || !p.LiteLLMDB.IsEnabled() {
		return nil
	}

	if logCtx == nil || logCtx.Credential == nil || logCtx.Request == nil {
		return nil
	}

	// Fallback to request ID if session ID not provided
	if logCtx.SessionID == "" {
		logCtx.SessionID = logCtx.RequestID
	}

	// Build model_id as credential.name:model_name
	// For proxy credentials, use the actual upstream credential name if available
	credName := logCtx.Credential.Name
	if logCtx.ActualCredentialName != "" {
		credName = logCtx.ActualCredentialName
	}
	modelIDFormatted := credName + ":" + logCtx.ModelID
	hashedToken := litellmdb.HashToken(logCtx.Token)

	// Extract user info from tokenInfo (or use empty strings as fallback)
	var userID, teamID, organizationID string
	if logCtx.TokenInfo != nil {
		userID = logCtx.TokenInfo.UserID
		teamID = logCtx.TokenInfo.TeamID
		organizationID = logCtx.TokenInfo.OrganizationID
	}

	// Determine end user - prefer user email from tokenInfo
	endUser := extractEndUser(logCtx.Request)
	if logCtx.TokenInfo != nil && logCtx.TokenInfo.UserEmail != "" {
		endUser = logCtx.TokenInfo.UserEmail
	}

	// Extract domain from targetURL for APIBase (e.g., "https://api.openai.com/..." -> "api.openai.com")
	apiBase := "auto_ai_router"
	if logCtx.TargetURL != "" {
		if u, err := url.Parse(logCtx.TargetURL); err == nil && u.Host != "" {
			apiBase = u.Host
		}
	}

	// Determine final status if not explicitly set
	status := logCtx.Status
	if status == "" {
		if logCtx.HTTPStatus >= 400 {
			status = "failure"
		} else {
			status = "success"
		}
	}

	// Ensure TokenUsage is not nil to prevent nil pointer dereference
	if logCtx.TokenUsage == nil {
		logCtx.TokenUsage = &converter.TokenUsage{}
	}

	// Calculate cost based on model pricing and token usage.
	// Try real model name first (from models[].model), then alias name.
	var cost float64
	var tokenCosts *converter.TokenCosts
	if p.priceRegistry == nil {
		p.logger.Warn("Price registry not available, using 0 cost for spend log")
	} else {
		priceModelID := logCtx.ModelID
		if logCtx.RealModelID != "" && logCtx.RealModelID != logCtx.ModelID {
			priceModelID = logCtx.RealModelID
		}
		modelPrice := p.priceRegistry.GetPrice(priceModelID)
		if modelPrice == nil && priceModelID != logCtx.ModelID {
			modelPrice = p.priceRegistry.GetPrice(logCtx.ModelID)
		}
		if modelPrice == nil {
			p.logger.Warn("Model price not found in registry, using 0 cost",
				"model_name", priceModelID)
		} else {
			tokenCosts = modelPrice.CalculateCosts(logCtx.TokenUsage)
			if tokenCosts != nil {
				cost = tokenCosts.TotalCost
			}
			p.logger.Debug("Calculated cost for model",
				"model_name", priceModelID,
				"cost", cost,
				"prompt_tokens", logCtx.TokenUsage.PromptTokens,
				"completion_tokens", logCtx.TokenUsage.CompletionTokens)
		}
	}

	// Build metadata with usage, cost breakdown, requester IP, and optional error
	requesterIP := getClientIP(logCtx.Request)
	metadata := buildMetadata(hashedToken, logCtx.TokenInfo, logCtx.ErrorMsg, logCtx.HTTPStatus, logCtx.TokenUsage, requesterIP, tokenCosts)

	customLLMProvider := strings.Replace(string(logCtx.Credential.Type), "-", "_", 1)
	if customLLMProvider == "proxy" {
		customLLMProvider = string(config.ProviderTypeOpenAI)
	}

	if teamID == "" {
		teamID = credName
	}

	return p.LiteLLMDB.LogSpend(&litellmdb.SpendLogEntry{
		RequestID:         logCtx.RequestID,
		StartTime:         logCtx.StartTime,
		EndTime:           utils.NowUTC(),
		CallType:          logCtx.Request.URL.Path,
		APIBase:           apiBase,
		Model:             logCtx.ModelID,    // Model name
		ModelID:           modelIDFormatted,  // credential.name:model_name
		ModelGroup:        logCtx.ModelID,    // Model name
		CustomLLMProvider: customLLMProvider, // Provider type as string
		PromptTokens:      logCtx.TokenUsage.PromptTokens,
		CompletionTokens:  logCtx.TokenUsage.CompletionTokens,
		TotalTokens:       logCtx.TokenUsage.Total(),
		Metadata:          metadata,
		Spend:             cost, // Calculated cost based on model pricing and token usage
		APIKey:            hashedToken,
		UserID:            userID,
		TeamID:            teamID,
		OrganizationID:    organizationID,
		EndUser:           endUser,
		RequesterIP:       getClientIP(logCtx.Request),
		Status:            status,
		SessionID:         logCtx.SessionID,
	})
}
