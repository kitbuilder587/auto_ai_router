package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mixaill76/auto_ai_router/internal/balancer"
	"github.com/mixaill76/auto_ai_router/internal/config"
	anthropicconv "github.com/mixaill76/auto_ai_router/internal/converter/anthropic"
	"github.com/mixaill76/auto_ai_router/internal/converter/openai"
	"github.com/mixaill76/auto_ai_router/internal/converter/responses"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/auth"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/users"
	"github.com/mixaill76/auto_ai_router/internal/responsestore"
	"github.com/mixaill76/auto_ai_router/internal/security"
)

type orchestratedRequest struct {
	request              *http.Request
	body                 []byte // body with realModelID substituted (for non-proxy providers)
	proxyBody            []byte // body with original modelID alias (for proxy forwarding)
	modelID              string // alias name (for rate limiting, credential lookup, logging)
	realModelID          string // real model name sent to provider (equals modelID if no alias configured)
	streaming            bool
	cred                 *config.CredentialConfig
	isResponsesAPI       bool
	convertedResp        bool
	passthroughResponses bool                         // true for codex/OpenAI models: Responses API forwarded as-is (no conversion)
	nativeResponses      bool                         // true when using Phase 4 ProviderResponses converter (Vertex/Anthropic)
	responsesMetadata    *responses.ResponsesMetadata // non-nil for Responses API requests
}

// orchestrateRequest performs auth and credential selection for an incoming request.
func (p *Proxy) orchestrateRequest(
	w http.ResponseWriter,
	r *http.Request,
	logCtx *RequestLogContext,
) (*orchestratedRequest, bool) {
	r = initializeRetryTrackingContext(r)

	isLiteLLMHealthy := p.isLiteLLMHealthy()

	if !p.authenticateRequest(w, r, logCtx, isLiteLLMHealthy) {
		return nil, false
	}

	body, modelID, realModelID, streaming, ok := p.readRequestBodyAndSelectModel(w, r, logCtx)
	if !ok {
		return nil, false
	}

	// proxyBody: body with the original alias restored.
	// Proxy credentials handle their own model routing, so they must receive the
	// alias ("anthropic/claude-sonnet-4.6"), not the provider-specific real name
	// ("global.anthropic.claude-sonnet-4-6") that was substituted for direct providers.
	proxyBody := body
	if modelID != realModelID {
		proxyBody = openai.ReplaceModelInBody(body, realModelID, modelID)
	}

	// Detect Responses API requests and select credential before conversion.
	isResponsesAPI := responses.IsResponsesAPI(body) && strings.Contains(r.URL.Path, "/responses")

	convertedResp := false
	passthroughResponses := false
	nativeResponses := false
	var responsesMetadata *responses.ResponsesMetadata
	var prevEntry *responsestore.StoredEntry
	preferredCredentialName := ""

	if isResponsesAPI {
		// Extract Responses-API-only metadata before the fields are deleted.
		meta := responses.ExtractResponsesMetadata(body)
		responsesMetadata = &meta

		if meta.PreviousResponseID != "" && p.responseStore != nil {
			apiKeyHash := litellmdb.HashToken(logCtx.Token)
			entry, loadErr := p.responseStore.GetEntry(r.Context(), meta.PreviousResponseID, apiKeyHash)
			if loadErr != nil {
				p.logger.Warn("Could not load previous_response_id, proceeding without history",
					"id", meta.PreviousResponseID, "error", loadErr)
			} else {
				prevEntry = entry
				preferredCredentialName = entry.CredentialName
			}
		}
	}

	cred, ok := p.selectCredentialForModel(w, modelID, logCtx.SessionID, preferredCredentialName, logCtx)
	if !ok {
		return nil, false
	}

	// Re-resolve the real model name for the selected credential.
	// This handles configs where the same alias (e.g. "claude-haiku-4.5") maps to a
	// different provider-specific real name on each credential
	// (e.g. "global.anthropic.claude-haiku-4-5-20251001-v1:0" on Bedrock vs
	// "anthropic/claude-haiku-4.5" on OpenRouter).
	// Proxy credentials handle their own model routing using the alias, so body is not
	// updated for them; proxyBody already holds the alias and is left unchanged.
	if cred.Type != config.ProviderTypeProxy {
		if credRealName, hasCredReal := p.modelManager.GetRealModelNameForCredential(modelID, cred.Name); hasCredReal && credRealName != realModelID {
			p.logger.Debug("Re-resolved real model name for credential",
				"alias", modelID,
				"old_real", realModelID,
				"new_real", credRealName,
				"credential", cred.Name,
			)
			body = openai.ReplaceModelInBody(body, realModelID, credRealName)
			realModelID = credRealName
		}
	}

	// Auto-inject Anthropic prompt-caching markers when a session is active.
	// This maximises cache hit rate when session-sticky routing keeps traffic on one credential.
	if p.stickyAutoCacheCtrl &&
		(cred.Type == config.ProviderTypeAnthropic || cred.Type == config.ProviderTypeBedrock) &&
		(logCtx.SessionID != "" || preferredCredentialName != "") {
		body = anthropicconv.InjectCacheControl(body)
	}

	p.logger.Debug("Responses API detection",
		"is_responses_api", isResponsesAPI,
		"provider", cred.Type,
		"model", modelID,
		"streaming", streaming,
		"url_path", r.URL.Path)

	if isResponsesAPI {
		// Handle previous_response_id: load the previous entry and prepend its
		// accumulated input + output so the model sees the full conversation history.
		prevEntryHandled := false
		if responsesMetadata.PreviousResponseID != "" && prevEntry != nil && prevEntry.ResponseJSON != nil {
			var accInput json.RawMessage
			if prevEntry.AccumulatedInput != nil {
				accInput = prevEntry.AccumulatedInput
			}
			newBody, prependErr := responses.PrependHistoryToInput(body, accInput, prevEntry.ResponseJSON.Output)
			if prependErr != nil {
				p.logger.Warn("Failed to prepend previous response history, ignoring",
					"id", responsesMetadata.PreviousResponseID, "error", prependErr)
			} else {
				body = newBody
				prevEntryHandled = true
				p.logger.Debug("Prepended previous response history to input",
					"previous_response_id", responsesMetadata.PreviousResponseID,
					"output_items", len(prevEntry.ResponseJSON.Output),
					"credential", preferredCredentialName,
				)
			}
		}

		// Capture the full accumulated input (history + current) for storage.
		// This must happen after any history prepending but before RequestToChat removes "input".
		responsesMetadata.AccumulatedInput = responses.ExtractInputArray(body)

		switch {
		case p.modelManager.IsPassthroughResponsesForProvider(modelID, cred.Type):
			// Passthrough: forward to the provider's native /v1/responses endpoint.
			// Default for OpenAI credentials; can be overridden per-model via
			// passthrough_responses: true/false in the models[] config section.
			// PrepareCodexPassthrough strips proxy-internal fields and normalises the
			// body to match what OpenAI's Responses API actually accepts.
			body = responses.PrepareCodexPassthrough(body, prevEntryHandled)
			proxyBody = responses.PrepareCodexPassthrough(proxyBody, prevEntryHandled)
			passthroughResponses = true
			p.logger.Debug("Native Responses API passthrough",
				"model", modelID, "provider", cred.Type, "streaming", streaming)

		case responses.HasNativeResponsesForModel(cred.Type, realModelID):
			// Provider has a native Responses converter (Vertex AI, Anthropic).
			// Keep body in Responses API format — it will be converted by
			// ProviderResponses.RequestFrom() in proxy.go.
			nativeResponses = true
			p.logger.Debug("Native Responses converter path",
				"model", modelID, "provider", cred.Type, "streaming", streaming)

		default:
			// Fallback: convert to Chat Completions format so all providers work uniformly.
			chatBody, convErr := responses.RequestToChat(body)
			if convErr != nil {
				p.logger.Error("Failed to convert Responses API request",
					"error_code", http.StatusBadRequest,
					"credential", cred.Name, "provider", string(cred.Type),
					"model", modelID, "error", convErr,
					"request_id", logCtx.RequestID)
				logCtx.Status = "failure"
				logCtx.HTTPStatus = http.StatusBadRequest
				logCtx.ErrorMsg = "Failed to convert Responses API request: " + convErr.Error()
				WriteErrorBadRequest(w, "Failed to convert Responses API request")
				return nil, false
			}
			// Re-apply model-specific parameter transformations now that the body is
			// in Chat Completions format.  RequestToChat maps max_output_tokens →
			// max_tokens; for reasoning models (o1/o3/o4/gpt-5) ReplaceBodyParam
			// renames max_tokens → max_completion_tokens and strips unsupported params.
			body = openai.ReplaceBodyParam(realModelID, chatBody)
			convertedResp = true
			// For streaming: inject stream_options.include_usage since extractMetadataFromBody
			// skipped it for Responses API (the original body had "input" not "messages").
			// Now that we've converted to Chat Completions format, providers need this.
			if streaming {
				body = injectStreamOptions(body)
			}
			// Rewrite URL path from /v1/responses to /v1/chat/completions
			// so passthrough providers (OpenAI, Proxy) send to the correct endpoint.
			r.URL.Path = strings.Replace(r.URL.Path, "/responses", "/chat/completions", 1)
			p.logger.Debug("Converted Responses API request to Chat Completions format",
				"model", modelID, "streaming", streaming)
		}
	}

	logCtx.Credential = cred
	r = markCredentialAsTried(r, cred.Name)

	return &orchestratedRequest{
		request:              r,
		body:                 body,
		proxyBody:            proxyBody,
		modelID:              modelID,
		realModelID:          realModelID,
		streaming:            streaming,
		cred:                 cred,
		isResponsesAPI:       isResponsesAPI,
		convertedResp:        convertedResp,
		passthroughResponses: passthroughResponses,
		nativeResponses:      nativeResponses,
		responsesMetadata:    responsesMetadata,
	}, true
}

func initializeRetryTrackingContext(r *http.Request) *http.Request {
	ctx := r.Context()
	ctx = SetTried(ctx, make(map[string]bool))
	return r.WithContext(ctx)
}

func markCredentialAsTried(r *http.Request, credentialName string) *http.Request {
	ctx := r.Context()
	triedCreds := GetTried(ctx)
	triedCreds[credentialName] = true
	ctx = SetTried(ctx, triedCreds)
	return r.WithContext(ctx)
}

func (p *Proxy) isLiteLLMHealthy() bool {
	if p.LiteLLMDB == nil || !p.LiteLLMDB.IsEnabled() {
		return false
	}
	if p.healthChecker != nil {
		return p.healthChecker.IsDBHealthy()
	}
	return p.LiteLLMDB.IsHealthy()
}

func (p *Proxy) authenticateRequest(
	w http.ResponseWriter,
	r *http.Request,
	logCtx *RequestLogContext,
	isLiteLLMHealthy bool,
) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		// Client-side error (bad request from the caller), not a service failure
		p.logger.Warn("Missing Authorization header",
			"error_code", http.StatusUnauthorized, "path", r.URL.Path)
		logCtx.Status = "failure"
		logCtx.HTTPStatus = http.StatusUnauthorized
		logCtx.ErrorMsg = "Missing Authorization header"
		WriteErrorUnauthorized(w, "Missing Authorization header")
		return false
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	logCtx.Token = token
	if token == authHeader {
		p.logger.Warn("Invalid Authorization header format",
			"error_code", http.StatusUnauthorized, "path", r.URL.Path)
		logCtx.Status = "failure"
		logCtx.HTTPStatus = http.StatusUnauthorized
		logCtx.ErrorMsg = "Invalid Authorization header format"
		WriteErrorUnauthorized(w, "Invalid Authorization header format")
		return false
	}

	if token == p.masterKey {
		logCtx.TokenInfo = &models.TokenInfo{Token: auth.HashToken(p.masterKey), KeyName: "litellm-master-key", UserID: "litellm-master-key"}
		return true
	}

	// JWT session token validation (tokens from /v2/login)
	if strings.HasPrefix(token, "eyJ") {
		claims, jwtErr := users.ValidateSessionJWT(token, p.masterKey)
		if jwtErr == nil && claims != nil {
			p.logger.Debug("Authenticated via session JWT",
				"user_id", claims.UserID,
				"user_role", claims.UserRole,
			)
			return true
		}
		// JWT validation failed — fall through to LiteLLM DB check
	}

	if isLiteLLMHealthy {
		tokenInfo, err := p.LiteLLMDB.ValidateToken(r.Context(), token)
		logCtx.TokenInfo = tokenInfo
		if err != nil {
			logCtx.Status = "failure"
			logCtx.HTTPStatus = http.StatusUnauthorized

			if p.handleLiteLLMAuthError(w, err, token) {
				logCtx.ErrorMsg = "LiteLLM auth validation failed"
			} else {
				logCtx.ErrorMsg = "LiteLLM DB unavailable"
			}
			return false
		} else if tokenInfo != nil {
			p.logger.Debug("Token validated via LiteLLM DB",
				"user_id", tokenInfo.UserID,
				"team_id", tokenInfo.TeamID,
			)
		}
		return true
	} else {
		p.logger.Warn("Invalid master key",
			"error_code", http.StatusUnauthorized,
			"provided_key_prefix", security.MaskAPIKey(token))
		WriteErrorUnauthorized(w, "Invalid master key")
	}

	return false
}

func (p *Proxy) readRequestBodyAndSelectModel(
	w http.ResponseWriter,
	r *http.Request,
	logCtx *RequestLogContext,
) ([]byte, string, string, bool, bool) {
	maxBodyBytes := int64(p.maxBodySizeMB) * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		// Client-side transport problem while sending the body
		p.logger.Warn("Failed to read request body",
			"error_code", http.StatusBadRequest, "error", err)
		logCtx.Status = "failure"
		logCtx.HTTPStatus = http.StatusBadRequest
		logCtx.ErrorMsg = "Failed to read request body: " + err.Error()
		WriteErrorBadRequest(w, "Failed to read request body")
		return nil, "", "", false, false
	}
	if closeErr := r.Body.Close(); closeErr != nil {
		p.logger.Warn("Failed to close request body", "error", closeErr)
	}
	if int64(len(body)) > maxBodyBytes {
		p.logger.Warn("Request body exceeds max size",
			"error_code", http.StatusRequestEntityTooLarge,
			"max_body_size_mb", p.maxBodySizeMB,
			"actual_size_bytes", len(body),
		)
		logCtx.Status = "failure"
		logCtx.HTTPStatus = http.StatusRequestEntityTooLarge
		logCtx.ErrorMsg = "Request body too large"
		WriteErrorTooLarge(w, "Request Entity Too Large")
		return nil, "", "", false, false
	}

	modelID, streaming, sessionID, body := extractMetadataFromBody(body, r.Header.Get("Content-Type"))
	logCtx.ModelID = modelID
	logCtx.SessionID = sessionID

	if modelID == "" {
		p.logger.Warn("Model not specified in request body",
			"error_code", http.StatusBadRequest, "path", r.URL.Path)
		logCtx.Status = "failure"
		logCtx.HTTPStatus = http.StatusBadRequest
		logCtx.ErrorMsg = "Model not specified in request body"
		WriteErrorBadRequest(w, "model field is required")
		return nil, "", "", false, false
	}

	// Resolve model_alias entries (changes modelID to real name; credential lookup uses real name)
	if resolved, isAlias := p.modelManager.ResolveAlias(modelID); isAlias {
		p.logger.Debug("Resolved model alias", "alias", modelID, "resolved", resolved)
		body = openai.ReplaceModelInBody(body, modelID, resolved)
		modelID = resolved
		logCtx.ModelID = modelID
	}

	// Resolve models[].model field: replace model in body for provider but keep alias as modelID
	// for rate limiting and credential lookup.
	realModelID := modelID
	if realName, hasReal := p.modelManager.GetRealModelName(modelID); hasReal {
		p.logger.Debug("Resolved model real name", "alias", modelID, "real", realName)
		body = openai.ReplaceModelInBody(body, modelID, realName)
		realModelID = realName
	}

	body = openai.ReplaceBodyParam(realModelID, body)

	return body, modelID, realModelID, streaming, true
}

func (p *Proxy) selectCredentialForModel(
	w http.ResponseWriter,
	modelID string,
	sessionID string,
	preferredCredentialName string,
	logCtx *RequestLogContext,
) (*config.CredentialConfig, bool) {
	if preferredCredentialName != "" {
		cred, err := p.balancer.NextSpecific(preferredCredentialName, modelID)
		if err == nil {
			p.logger.Debug("Responses API sticky routing: using credential from previous_response_id",
				"credential", cred.Name,
				"model", modelID,
			)
			return cred, true
		}

		p.logger.Debug("Responses API sticky routing: previous_response credential unavailable, falling back to standard selection",
			"credential", preferredCredentialName,
			"model", modelID,
			"error", err,
		)
	}

	if sessionID != "" && p.sessionStore != nil {
		if credName, ok := p.sessionStore.Get(sessionID, modelID); ok {
			cred, err := p.balancer.NextSpecific(credName, modelID)
			if err == nil {
				p.logger.Debug("Session-sticky routing: using cached credential",
					"session_id", sessionID,
					"credential", cred.Name,
					"model", modelID,
				)
				return cred, true
			}

			p.logger.Debug("Session-sticky routing: cached credential unavailable, falling back to standard selection",
				"session_id", sessionID,
				"credential", credName,
				"model", modelID,
				"error", err,
			)
		}
	}

	cred, err := p.balancer.NextForModel(modelID)
	if err == nil {
		return cred, true
	}

	fallbackErr := error(nil)
	cred, fallbackErr = p.balancer.NextFallbackForModel(modelID)
	if fallbackErr == nil {
		return cred, true
	}

	errCode := http.StatusTooManyRequests
	var errorMsg string
	if errors.Is(err, balancer.ErrRateLimitExceeded) || errors.Is(fallbackErr, balancer.ErrRateLimitExceeded) {
		errorMsg = "Rate limit exceeded"
	} else {
		errorMsg = fmt.Sprintf("No credentials available for model %s", modelID)
	}

	p.logger.Error("No credentials available (regular and fallback)",
		"error_code", errCode,
		"model", modelID,
		"primary_error", err,
		"fallback_error", fallbackErr,
		"request_id", logCtx.RequestID,
	)

	logCtx.Status = "failure"
	logCtx.HTTPStatus = errCode
	logCtx.ErrorMsg = errorMsg
	logCtx.Credential = &config.CredentialConfig{
		Name: "system",
		Type: config.ProviderTypeProxy,
	}

	if err := p.logSpendToLiteLLMDB(logCtx); err != nil {
		p.logger.Warn("Failed to queue error log for no credentials",
			"error", err,
			"request_id", logCtx.RequestID,
		)
	}
	logCtx.Logged = true

	WriteErrorRateLimit(w, errorMsg)
	return nil, false
}
