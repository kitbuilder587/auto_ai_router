package balancer

import (
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/fail2ban"
	"github.com/mixaill76/auto_ai_router/internal/monitoring"
	"github.com/mixaill76/auto_ai_router/internal/ratelimit"
	"github.com/mixaill76/auto_ai_router/internal/scopes"
)

// ModelChecker interface for checking model availability
type ModelChecker interface {
	HasModel(credentialName, modelID string) bool
	GetCredentialsForModel(modelID string) []string
	GetModelWeightForCredential(modelID, credentialName string) int
	IsEnabled() bool
}

var (
	ErrNoCredentialsAvailable = errors.New("no credentials available")
	ErrRateLimitExceeded      = errors.New("rate limit exceeded")
)

type candidateEntry struct {
	absIdx int
	cred   *config.CredentialConfig
}

type RoundRobin struct {
	mu              sync.RWMutex
	credentials     []config.CredentialConfig
	staticCreds     []config.CredentialConfig // immutable snapshot of YAML-defined credentials
	credentialIndex map[string]int            // O(1) lookup by name instead of O(n) search
	swrr            map[schedKey]*swrrState   // smooth weighted round-robin state per selection cycle
	fail2ban        *fail2ban.Fail2Ban
	rateLimiter     *ratelimit.RPMLimiter
	modelChecker    ModelChecker
	logger          *slog.Logger
}

func New(credentials []config.CredentialConfig, f2b *fail2ban.Fail2Ban, rl *ratelimit.RPMLimiter) *RoundRobin {
	if f2b == nil {
		panic("balancer.New: fail2ban must not be nil")
	}
	if rl == nil {
		panic("balancer.New: rateLimiter must not be nil")
	}

	credentialIndex := make(map[string]int, len(credentials))
	for i, c := range credentials {
		// Normalize TPM: 0 means "not configured" → treat as unlimited (-1).
		// Convention: -1 = unlimited, positive = limit.
		tpm := c.TPM
		if tpm == 0 {
			tpm = -1
		}
		rl.AddCredentialWithTPM(c.Name, c.RPM, tpm)
		credentialIndex[c.Name] = i
	}

	rr := &RoundRobin{
		credentials:     credentials,
		staticCreds:     append([]config.CredentialConfig(nil), credentials...),
		credentialIndex: credentialIndex,
		swrr:            make(map[schedKey]*swrrState),
		fail2ban:        f2b,
		rateLimiter:     rl,
		modelChecker:    nil,
		logger:          slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}

	// Validate fallback configuration (cycle detection and unused fallback detection)
	rr.validateFallbackConfiguration()

	return rr
}

// SetLogger sets the logger for the RoundRobin balancer
func (r *RoundRobin) SetLogger(logger *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger = logger
}

// SetModelChecker sets the model checker for filtering credentials by model
func (r *RoundRobin) SetModelChecker(mc ModelChecker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modelChecker = mc
}

// getCredentialByName finds a credential by name (must be called with lock held)
func (r *RoundRobin) getCredentialByName(name string) *config.CredentialConfig {
	idx, ok := r.credentialIndex[name]
	if !ok {
		return nil
	}
	return &r.credentials[idx]
}

// IsProxyCredential checks if a credential is a proxy type
func (r *RoundRobin) IsProxyCredential(credentialName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cred := r.getCredentialByName(credentialName)
	return cred != nil && cred.Type == config.ProviderTypeProxy
}

// IsBanned checks if a specific credential+model pair is currently banned
func (r *RoundRobin) IsBanned(credentialName, modelID string) bool {
	return r.fail2ban.IsBanned(credentialName, modelID)
}

// HasAnyBan checks if a credential has any banned models
func (r *RoundRobin) HasAnyBan(credentialName string) bool {
	return r.fail2ban.HasAnyBan(credentialName)
}

// GetProxyCredentials returns all proxy type credentials
func (r *RoundRobin) GetProxyCredentials() []config.CredentialConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var proxies []config.CredentialConfig
	for _, cred := range r.credentials {
		if cred.Type == config.ProviderTypeProxy {
			proxies = append(proxies, cred)
		}
	}
	return proxies
}

// NextForModel returns the next available credential that supports the specified model
func (r *RoundRobin) NextForModel(modelID string) (*config.CredentialConfig, error) {
	return r.next(modelID, false, false)
}

func (r *RoundRobin) NextForModelWithScopes(modelID string, requestScopes scopes.Set) (*config.CredentialConfig, error) {
	return r.nextWithScopes(modelID, false, false, requestScopes)
}

// NextFallbackForModel returns the next available fallback credential
func (r *RoundRobin) NextFallbackForModel(modelID string) (*config.CredentialConfig, error) {
	return r.next(modelID, true, false)
}

func (r *RoundRobin) NextFallbackForModelWithScopes(modelID string, requestScopes scopes.Set) (*config.CredentialConfig, error) {
	return r.nextWithScopes(modelID, true, false, requestScopes)
}

// NextFallbackProxyForModel returns the next available fallback proxy credential
func (r *RoundRobin) NextFallbackProxyForModel(modelID string) (*config.CredentialConfig, error) {
	return r.next(modelID, true, true)
}

func (r *RoundRobin) NextFallbackProxyForModelWithScopes(modelID string, requestScopes scopes.Set) (*config.CredentialConfig, error) {
	return r.nextWithScopes(modelID, true, true, requestScopes)
}

// NextSpecific tries to return a specific credential by name without advancing the
// round-robin state. It still applies model availability, ban, and rate-limit checks.
func (r *RoundRobin) NextSpecific(credentialName, modelID string) (*config.CredentialConfig, error) {
	return r.NextSpecificWithScopes(credentialName, modelID, scopes.All())
}

func (r *RoundRobin) NextSpecificWithScopes(credentialName, modelID string, requestScopes scopes.Set) (*config.CredentialConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	idx, ok := r.credentialIndex[credentialName]
	if !ok {
		return nil, ErrNoCredentialsAvailable
	}

	cred := &r.credentials[idx]
	if !scopes.Allows(cred.Scopes, requestScopes) {
		return nil, ErrNoCredentialsAvailable
	}

	if modelID != "" && r.modelChecker != nil && r.modelChecker.IsEnabled() {
		if !r.modelChecker.HasModel(credentialName, modelID) {
			return nil, ErrNoCredentialsAvailable
		}
	}

	if r.fail2ban.IsBanned(credentialName, modelID) {
		return nil, ErrNoCredentialsAvailable
	}

	if !r.rateLimiter.TryAllowAll(credentialName, modelID) {
		return nil, ErrRateLimitExceeded
	}

	return cred, nil
}

func (r *RoundRobin) next(modelID string, allowOnlyFallback, allowOnlyProxy bool) (*config.CredentialConfig, error) {
	return r.nextWithScopes(modelID, allowOnlyFallback, allowOnlyProxy, scopes.All())
}

func (r *RoundRobin) nextWithScopes(modelID string, allowOnlyFallback, allowOnlyProxy bool, requestScopes scopes.Set) (*config.CredentialConfig, error) {
	return r.nextExcludingWithScopes(modelID, allowOnlyFallback, allowOnlyProxy, "", nil, requestScopes)
}

// nextExcluding is the core credential selection logic with optional exclude set.
// Excluded credentials are skipped entirely and don't count as candidates.
//
// The algorithm runs in three phases:
//  1. Build a candidate list via structural filters (exclude, type/fallback, model availability).
//     These are time-stable properties — they don't change between requests.
//  2. Drop banned candidates, then pick by smooth weighted round-robin per selection cycle.
//  3. Commit the highest-priority candidate that passes its rate limits.
func (r *RoundRobin) nextExcluding(modelID string, allowOnlyFallback, allowOnlyProxy bool, requiredType config.ProviderType, exclude map[string]bool) (*config.CredentialConfig, error) {
	return r.nextExcludingWithScopes(modelID, allowOnlyFallback, allowOnlyProxy, requiredType, exclude, scopes.All())
}

func (r *RoundRobin) nextExcludingWithScopes(modelID string, allowOnlyFallback, allowOnlyProxy bool, requiredType config.ProviderType, exclude map[string]bool, requestScopes scopes.Set) (*config.CredentialConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Phase 1: Build candidate list using only structural (time-stable) filters.
	var candidates []candidateEntry

	for i := range r.credentials {
		cred := &r.credentials[i]

		if len(exclude) > 0 && exclude[cred.Name] {
			continue
		}

		if !scopes.Allows(cred.Scopes, requestScopes) {
			monitoring.CredentialSelectionRejected.WithLabelValues("scope_not_allowed").Inc()
			continue
		}

		if allowOnlyProxy && cred.Type != config.ProviderTypeProxy {
			monitoring.CredentialSelectionRejected.WithLabelValues("type_not_allowed").Inc()
			continue
		}

		if requiredType != "" && cred.Type != requiredType {
			monitoring.CredentialSelectionRejected.WithLabelValues("type_mismatch").Inc()
			continue
		}

		if allowOnlyFallback {
			if !cred.IsFallback {
				monitoring.CredentialSelectionRejected.WithLabelValues("fallback_not_available").Inc()
				continue
			}
		} else if cred.IsFallback {
			monitoring.CredentialSelectionRejected.WithLabelValues("fallback_only").Inc()
			continue
		}

		// Check model availability before ban/rate checks.
		// model_not_available is a structural property, not a temporary issue.
		if modelID != "" && r.modelChecker != nil && r.modelChecker.IsEnabled() {
			if !r.modelChecker.HasModel(cred.Name, modelID) {
				monitoring.CredentialSelectionRejected.WithLabelValues("model_not_available").Inc()
				continue
			}
		}

		candidates = append(candidates, candidateEntry{absIdx: i, cred: cred})
	}

	if len(candidates) == 0 {
		return nil, ErrNoCredentialsAvailable
	}

	// Phase 2: Smooth weighted round-robin over candidates that are available now.
	// Banned/rate-limited credentials are dropped here (not in Phase 1) so they don't
	// accumulate weight while down — otherwise a high-weight provider would burst on recovery.
	// With equal weights this degenerates to the historical round-robin sequence.
	state := r.swrrStateFor(r.schedKeyFor(modelID, allowOnlyFallback, allowOnlyProxy, requiredType, hasActiveExclusion(exclude)))

	liveWeights := make(map[string]int, len(candidates))
	live := make([]candidateEntry, 0, len(candidates))
	rateLimitHit := false
	for _, c := range candidates {
		if r.fail2ban.IsBanned(c.cred.Name, modelID) {
			monitoring.CredentialSelectionRejected.WithLabelValues("banned").Inc()
			continue
		}
		if !r.canPassRateLimits(c.cred.Name, modelID) {
			monitoring.CredentialSelectionRejected.WithLabelValues("rate_limit").Inc()
			rateLimitHit = true
			continue
		}
		liveWeights[c.cred.Name] = r.effectiveWeight(c.cred, modelID)
		live = append(live, c)
	}
	if len(live) == 0 {
		if rateLimitHit {
			return nil, ErrRateLimitExceeded
		}
		return nil, ErrNoCredentialsAvailable
	}

	total := state.advance(liveWeights)

	// Order by running counter (desc); ties keep the structural candidate order so that
	// equal weights reproduce the historical ascending round-robin sequence.
	sort.SliceStable(live, func(i, j int) bool {
		return state.currentOf(live[i].cred.Name) > state.currentOf(live[j].cred.Name)
	})

	// Phase 3: Commit the highest-priority candidate that passes its rate limits.
	// TryAllowAll atomically checks credential + model RPM/TPM and records usage only on
	// success, preventing TOCTOU races after the non-recording precheck above.
	for _, c := range live {
		if !r.rateLimiter.TryAllowAll(c.cred.Name, modelID) {
			monitoring.CredentialSelectionRejected.WithLabelValues("rate_limit").Inc()
			rateLimitHit = true
			continue
		}
		state.commit(c.cred.Name, total)
		return c.cred, nil
	}

	// Prioritize rate limit error: if any candidate hit rate limit, surface it even if
	// others were banned. This gives callers accurate signal for backoff/retry logic.
	if rateLimitHit {
		return nil, ErrRateLimitExceeded
	}
	// All candidates are banned (or none remain after ban + rate-limit filtering).
	return nil, ErrNoCredentialsAvailable
}

func (r *RoundRobin) canPassRateLimits(credentialName, modelID string) bool {
	if !r.rateLimiter.CanAllow(credentialName) || !r.rateLimiter.AllowTokens(credentialName) {
		return false
	}
	if modelID == "" {
		return true
	}
	return r.rateLimiter.CanAllowModel(credentialName, modelID) &&
		r.rateLimiter.AllowModelTokens(credentialName, modelID)
}

func hasActiveExclusion(exclude map[string]bool) bool {
	for _, excluded := range exclude {
		if excluded {
			return true
		}
	}
	return false
}

// NextForModelExcluding returns the next available non-fallback credential that supports
// the specified model, excluding credentials in the exclude set.
func (r *RoundRobin) NextForModelExcluding(modelID string, exclude map[string]bool) (*config.CredentialConfig, error) {
	return r.nextExcluding(modelID, false, false, "", exclude)
}

func (r *RoundRobin) NextForModelExcludingWithScopes(modelID string, exclude map[string]bool, requestScopes scopes.Set) (*config.CredentialConfig, error) {
	return r.nextExcludingWithScopes(modelID, false, false, "", exclude, requestScopes)
}

// NextSameTypeForModelExcluding returns the next available non-fallback credential of the
// same type as credType, excluding credentials in the exclude set. Used for same-type
// credential retry on provider errors (429/5xx/auth errors) to prevent cross-type routing.
func (r *RoundRobin) NextSameTypeForModelExcluding(modelID string, credType config.ProviderType, exclude map[string]bool) (*config.CredentialConfig, error) {
	return r.NextSameTypeForModelExcludingWithScopes(modelID, credType, exclude, scopes.All())
}

func (r *RoundRobin) NextSameTypeForModelExcludingWithScopes(modelID string, credType config.ProviderType, exclude map[string]bool, requestScopes scopes.Set) (*config.CredentialConfig, error) {
	if credType == config.ProviderTypeProxy {
		// allowOnlyProxy=true already restricts to proxy type
		return r.nextExcludingWithScopes(modelID, false, true, "", exclude, requestScopes)
	}
	return r.nextExcludingWithScopes(modelID, false, false, credType, exclude, requestScopes)
}

func (r *RoundRobin) RecordResponse(credentialName, modelID string, statusCode int) {
	r.fail2ban.RecordResponse(credentialName, modelID, statusCode)
}

func (r *RoundRobin) GetCredentialsSnapshot() []config.CredentialConfig {
	return r.GetCredentialsSnapshotWithScopes(scopes.All())
}

func (r *RoundRobin) GetCredentialsSnapshotWithScopes(requestScopes scopes.Set) []config.CredentialConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	creds := make([]config.CredentialConfig, 0, len(r.credentials))
	for _, cred := range r.credentials {
		if scopes.Allows(cred.Scopes, requestScopes) {
			creds = append(creds, cred)
		}
	}
	return creds
}

func (r *RoundRobin) GetAvailableCount() int {
	return r.GetAvailableCountWithScopes(scopes.All())
}

func (r *RoundRobin) GetAvailableCountWithScopes(requestScopes scopes.Set) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, cred := range r.credentials {
		if !scopes.Allows(cred.Scopes, requestScopes) {
			continue
		}
		if !r.fail2ban.HasAnyBan(cred.Name) {
			count++
		}
	}
	return count
}

func (r *RoundRobin) GetBannedCount() int {
	return r.fail2ban.GetBannedCount()
}

func (r *RoundRobin) GetBannedCountWithScopes(requestScopes scopes.Set) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, cred := range r.credentials {
		if scopes.Allows(cred.Scopes, requestScopes) && r.fail2ban.HasAnyBan(cred.Name) {
			count++
		}
	}
	return count
}

// GetBannedPairs returns all currently banned credential+model pairs with error details
func (r *RoundRobin) GetBannedPairs() []fail2ban.BanPair {
	return r.fail2ban.GetBannedPairs()
}

// UpdateDBCredentials atomically replaces the DB-sourced portion of the credential list.
// Static (YAML-defined) credentials are always preserved unchanged.
// New credentials are registered in the rate limiter; stale entries are left in the rate
// limiter but will never be selected since they are absent from the credential list.
func (r *RoundRobin) UpdateDBCredentials(dbCreds []config.CredentialConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Build name set of static creds so we can skip duplicates from DB.
	staticNames := make(map[string]bool, len(r.staticCreds))
	for _, c := range r.staticCreds {
		staticNames[c.Name] = true
	}

	// Filter out DB creds that clash with static names.
	filtered := make([]config.CredentialConfig, 0, len(dbCreds))
	for _, c := range dbCreds {
		if !staticNames[c.Name] {
			filtered = append(filtered, c)
		}
	}

	// Merge static + new DB creds.
	newCreds := append(append([]config.CredentialConfig(nil), r.staticCreds...), filtered...)
	if len(newCreds) == 0 {
		// Nothing to update — keep existing credentials to avoid empty-list panics.
		return
	}

	// Upsert rate-limiter limits for all DB creds (not just new ones).
	// AddCredentialWithTPM overwrites the existing entry, so calling it every sync
	// guarantees that RPM/TPM changes in DB are picked up immediately.
	for _, c := range filtered {
		tpm := c.TPM
		if tpm == 0 {
			tpm = -1
		}
		r.rateLimiter.AddCredentialWithTPM(c.Name, c.RPM, tpm)
	}

	// Rebuild the O(1) index.
	newIndex := make(map[string]int, len(newCreds))
	for i, c := range newCreds {
		newIndex[c.Name] = i
	}

	r.credentials = newCreds
	r.credentialIndex = newIndex
}

// validateFallbackConfiguration validates fallback credential configuration
// Logs count of fallback credentials
func (r *RoundRobin) validateFallbackConfiguration() {
	fallbackCount := 0
	for _, cred := range r.credentials {
		if cred.IsFallback {
			fallbackCount++
		}
	}

	if fallbackCount == 0 {
		r.logger.Info("No fallback credentials configured")
	} else {
		r.logger.Info("Fallback credential validation completed",
			"total_credentials", len(r.credentials),
			"fallback_credentials", fallbackCount,
		)
	}
}
