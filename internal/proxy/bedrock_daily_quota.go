package proxy

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/utils"
)

const (
	bedrockDailyQuotaProviderError = "bedrock_daily_token_quota_exhausted"

	bedrockDailyQuotaPhaseRetryAfter  = "retry_after"
	bedrockDailyQuotaPhaseResetWindow = "reset_window"
	bedrockDailyQuotaPhaseBackoff     = "backoff"

	bedrockDailyQuotaDeadlineRetryAfter = "retry_after"
	bedrockDailyQuotaDeadlineHeuristic  = "heuristic"

	bedrockDailyQuotaBodyScanLimit = 8 * 1024
)

var bedrockDailyQuotaResetHours = [...]int{4, 5, 6, 7, 8}

type bedrockDailyQuotaKey struct {
	credential string
	model      string
}

type bedrockDailyQuotaDecision struct {
	BanUntil time.Time
	Phase    string
	Source   string
	Attempt  int
}

type bedrockDailyQuotaState struct {
	decision       bedrockDailyQuotaDecision
	backoffAttempt int
}

type bedrockDailyQuotaTracker struct {
	mu     sync.Mutex
	states map[bedrockDailyQuotaKey]*bedrockDailyQuotaState
}

func newBedrockDailyQuotaTracker() *bedrockDailyQuotaTracker {
	return &bedrockDailyQuotaTracker{states: make(map[bedrockDailyQuotaKey]*bedrockDailyQuotaState)}
}

func isBedrockDailyTokenQuotaError(provider config.ProviderType, statusCode int, body []byte) bool {
	if provider != config.ProviderTypeBedrock || statusCode != http.StatusTooManyRequests || len(body) == 0 {
		return false
	}
	if len(body) > bedrockDailyQuotaBodyScanLimit {
		body = body[:bedrockDailyQuotaBodyScanLimit]
	}
	lowerBody := bytes.ToLower(body)
	return bytes.Contains(lowerBody, []byte("throttlingexception")) &&
		bytes.Contains(lowerBody, []byte("too many tokens per day"))
}

func parseRetryAfter(value string, now time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}

	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return time.Time{}, false
		}
		return now.UTC().Add(time.Duration(seconds) * time.Second), true
	}

	deadline, err := http.ParseTime(value)
	if err != nil {
		return time.Time{}, false
	}
	deadline = deadline.UTC()
	if !deadline.After(now.UTC()) {
		return time.Time{}, false
	}
	return deadline, true
}

func (t *bedrockDailyQuotaTracker) nextBan(credential, model string, now time.Time, retryAfter string) bedrockDailyQuotaDecision {
	now = now.UTC()
	key := bedrockDailyQuotaKey{credential: credential, model: model}

	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.states[key]
	if deadline, ok := parseRetryAfter(retryAfter, now); ok {
		if exists && state.decision.BanUntil.After(now) && !deadline.After(state.decision.BanUntil) {
			return state.decision
		}
		if !exists {
			state = &bedrockDailyQuotaState{}
			t.states[key] = state
		}
		state.decision = bedrockDailyQuotaDecision{
			BanUntil: deadline,
			Phase:    bedrockDailyQuotaPhaseRetryAfter,
			Source:   bedrockDailyQuotaDeadlineRetryAfter,
		}
		return state.decision
	}

	if exists && state.decision.BanUntil.After(now) {
		return state.decision
	}
	if !exists {
		state = &bedrockDailyQuotaState{}
		t.states[key] = state
	}

	if deadline, ok := nextBedrockDailyQuotaResetCheckpoint(now); ok {
		state.backoffAttempt = 0
		state.decision = bedrockDailyQuotaDecision{
			BanUntil: deadline,
			Phase:    bedrockDailyQuotaPhaseResetWindow,
			Source:   bedrockDailyQuotaDeadlineHeuristic,
		}
		return state.decision
	}

	if !exists {
		state.decision = bedrockDailyQuotaDecision{
			BanUntil: nextBedrockDailyQuotaUTCReset(now),
			Phase:    bedrockDailyQuotaPhaseResetWindow,
			Source:   bedrockDailyQuotaDeadlineHeuristic,
		}
		return state.decision
	}

	state.backoffAttempt++
	delay := time.Hour
	switch state.backoffAttempt {
	case 2:
		delay = 2 * time.Hour
	default:
		if state.backoffAttempt >= 3 {
			delay = 4 * time.Hour
		}
	}
	deadline := now.Add(delay)
	if nextUTCReset := nextBedrockDailyQuotaUTCReset(now); nextUTCReset.Before(deadline) {
		deadline = nextUTCReset
	}
	state.decision = bedrockDailyQuotaDecision{
		BanUntil: deadline,
		Phase:    bedrockDailyQuotaPhaseBackoff,
		Source:   bedrockDailyQuotaDeadlineHeuristic,
		Attempt:  state.backoffAttempt,
	}
	return state.decision
}

func (t *bedrockDailyQuotaTracker) reset(credential, model string) {
	t.mu.Lock()
	delete(t.states, bedrockDailyQuotaKey{credential: credential, model: model})
	t.mu.Unlock()
}

func (p *Proxy) recordProviderResponse(
	ctx context.Context,
	credential *config.CredentialConfig,
	model string,
	statusCode int,
	headers http.Header,
	body []byte,
) bool {
	if credential == nil {
		return false
	}

	classificationBody := body
	if len(body) > 0 && headers != nil {
		classificationBody = []byte(decodeResponseBody(body, headers.Get("Content-Encoding")))
	}
	if isBedrockDailyTokenQuotaError(credential.Type, statusCode, classificationBody) {
		retryAfter := ""
		if headers != nil {
			retryAfter = headers.Get("Retry-After")
		}
		decision := p.bedrockDailyQuota.nextBan(credential.Name, model, utils.NowUTC(), retryAfter)
		p.balancer.BanUntil(
			credential.Name,
			model,
			statusCode,
			decision.BanUntil,
			bedrockDailyQuotaProviderError,
		)
		p.logger.ErrorContext(ctx, "Bedrock daily token quota exhausted; route banned",
			"error_code", statusCode,
			"credential", credential.Name,
			"provider", string(credential.Type),
			"model", model,
			"provider_error", bedrockDailyQuotaProviderError,
			"recovery_phase", decision.Phase,
			"deadline_source", decision.Source,
			"retry_attempt", decision.Attempt,
			"ban_until", decision.BanUntil)
		return true
	}

	if credential.Type == config.ProviderTypeBedrock && statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		p.bedrockDailyQuota.reset(credential.Name, model)
	}
	p.balancer.RecordResponse(credential.Name, model, statusCode)
	return false
}

func nextBedrockDailyQuotaResetCheckpoint(now time.Time) (time.Time, bool) {
	now = now.UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	utcReset := startOfDay.Add(time.Minute)
	if now.Before(utcReset) {
		return utcReset, true
	}
	for _, hour := range bedrockDailyQuotaResetHours {
		checkpoint := startOfDay.Add(time.Duration(hour)*time.Hour + time.Minute)
		if now.Before(checkpoint) {
			return checkpoint, true
		}
	}
	return time.Time{}, false
}

func nextBedrockDailyQuotaUTCReset(now time.Time) time.Time {
	now = now.UTC()
	nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return nextDay.Add(time.Minute)
}
