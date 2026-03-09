package ratelimit

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// RPMLimiter tracks and enforces RPM (Requests Per Minute) and TPM (Tokens Per Minute) limits.
// Use this for API rate limiting where you need to track usage against configurable limits.
//
// Different from TimeBasedRateLimiter:
// - RPMLimiter: tracks usage against RPM/TPM limits (e.g., allow max 100 requests per minute)
// - TimeBasedRateLimiter: enforces fixed minimum interval (e.g., wait 100ms between requests)
//
// Counter storage is delegated to a counterBackend (local in-process or distributed Redis).
// The RPMLimiter itself only stores the configured limits.
type RPMLimiter struct {
	mu          sync.RWMutex
	limits      map[string]*limiterConfig // credential name → limits
	modelLimits map[string]*limiterConfig // "credential:model" → limits
	backend     counterBackend
}

// limiterConfig holds the configured RPM and TPM limits for one key.
// -1 means unlimited.
type limiterConfig struct {
	rpm int
	tpm int
}

// tokenUsage is kept here (used by local_backend.go in the same package).
type tokenUsage struct {
	timestamp time.Time
	count     int
}

// MaxRequestsBufferSize limits the maximum number of request timestamps stored in the
// local backend. This prevents unbounded memory growth.
const MaxRequestsBufferSize = 10_000_000

// MaxTokensBufferSize limits the maximum number of token records stored in the
// local backend.
const MaxTokensBufferSize = 10_000_000

// New creates a new RPMLimiter backed by the in-process local backend.
func New() *RPMLimiter {
	return &RPMLimiter{
		limits:      make(map[string]*limiterConfig),
		modelLimits: make(map[string]*limiterConfig),
		backend:     newLocalBackend(),
	}
}

// NewWithRedis creates an RPMLimiter that stores counters in Redis/Valkey.
func NewWithRedis(b *RedisBackend) *RPMLimiter {
	return &RPMLimiter{
		limits:      make(map[string]*limiterConfig),
		modelLimits: make(map[string]*limiterConfig),
		backend:     b,
	}
}

// makeModelKey creates a composite key for a (credential, model) pair.
func makeModelKey(credentialName, modelName string) string {
	return fmt.Sprintf("%s:%s", credentialName, modelName)
}

// credKey returns the backend counter key for a credential.
func credKey(credName string) string { return "c:" + credName }

// modelCounterKey returns the backend counter key for a (credential, model) pair.
func modelCounterKey(credName, modelName string) string {
	return "m:" + makeModelKey(credName, modelName)
}

func (r *RPMLimiter) getCredentialConfig(name string) *limiterConfig {
	r.mu.RLock()
	cfg := r.limits[name]
	r.mu.RUnlock()
	return cfg
}

func (r *RPMLimiter) getModelConfig(credName, modelName string) *limiterConfig {
	r.mu.RLock()
	cfg := r.modelLimits[makeModelKey(credName, modelName)]
	r.mu.RUnlock()
	return cfg
}

func (r *RPMLimiter) AddCredential(name string, rpm int) {
	r.AddCredentialWithTPM(name, rpm, -1)
}

func (r *RPMLimiter) AddCredentialWithTPM(name string, rpm int, tpm int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limits[name] = &limiterConfig{rpm: rpm, tpm: tpm}
}

func (r *RPMLimiter) AddModel(credentialName, modelName string, rpm int) {
	r.AddModelWithTPM(credentialName, modelName, rpm, -1)
}

func (r *RPMLimiter) AddModelWithTPM(credentialName, modelName string, rpm int, tpm int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := makeModelKey(credentialName, modelName)
	r.modelLimits[key] = &limiterConfig{rpm: rpm, tpm: tpm}
}

// Allow checks if a request for credentialName is allowed (RPM) and records it.
func (r *RPMLimiter) Allow(credentialName string) bool {
	cfg := r.getCredentialConfig(credentialName)
	if cfg == nil {
		return false
	}
	return r.backend.tryAllowRPM(credKey(credentialName), cfg.rpm)
}

// CanAllow checks whether a request would be allowed without recording it.
func (r *RPMLimiter) CanAllow(credentialName string) bool {
	cfg := r.getCredentialConfig(credentialName)
	if cfg == nil {
		return false
	}
	return r.backend.canAllowRPM(credKey(credentialName), cfg.rpm)
}

// AllowModel checks model-level RPM and records if allowed.
// Returns true if the model is not tracked (no limit configured).
func (r *RPMLimiter) AllowModel(credentialName, modelName string) bool {
	cfg := r.getModelConfig(credentialName, modelName)
	if cfg == nil {
		return true
	}
	return r.backend.tryAllowRPM(modelCounterKey(credentialName, modelName), cfg.rpm)
}

// CanAllowModel checks model-level RPM without recording.
// Returns true if the model is not tracked.
func (r *RPMLimiter) CanAllowModel(credentialName, modelName string) bool {
	cfg := r.getModelConfig(credentialName, modelName)
	if cfg == nil {
		return true
	}
	return r.backend.canAllowRPM(modelCounterKey(credentialName, modelName), cfg.rpm)
}

// AllowTokens checks whether the credential TPM limit permits further requests.
func (r *RPMLimiter) AllowTokens(credentialName string) bool {
	cfg := r.getCredentialConfig(credentialName)
	if cfg == nil {
		return false
	}
	return r.backend.canAllowTPM(credKey(credentialName), cfg.tpm)
}

// AllowModelTokens checks whether the model TPM limit permits further requests.
func (r *RPMLimiter) AllowModelTokens(credentialName, modelName string) bool {
	cfg := r.getModelConfig(credentialName, modelName)
	if cfg == nil {
		return true
	}
	return r.backend.canAllowTPM(modelCounterKey(credentialName, modelName), cfg.tpm)
}

// ConsumeTokens records token usage for a credential.
func (r *RPMLimiter) ConsumeTokens(credentialName string, tokenCount int) {
	if r.getCredentialConfig(credentialName) == nil {
		return
	}
	r.backend.consumeTokens(credKey(credentialName), tokenCount)
}

// ConsumeModelTokens records token usage for a model within a credential.
func (r *RPMLimiter) ConsumeModelTokens(credentialName, modelName string, tokenCount int) {
	if r.getModelConfig(credentialName, modelName) == nil {
		return
	}
	r.backend.consumeTokens(modelCounterKey(credentialName, modelName), tokenCount)
}

// GetCurrentRPM returns the number of requests in the last 60 seconds for a credential.
func (r *RPMLimiter) GetCurrentRPM(credentialName string) int {
	if r.getCredentialConfig(credentialName) == nil {
		return 0
	}
	return r.backend.currentRPM(credKey(credentialName))
}

// GetCurrentTPM returns the sum of tokens in the last 60 seconds for a credential.
func (r *RPMLimiter) GetCurrentTPM(credentialName string) int {
	if r.getCredentialConfig(credentialName) == nil {
		return 0
	}
	return r.backend.currentTPM(credKey(credentialName))
}

// GetCurrentModelRPM returns the RPM for a (credential, model) pair.
func (r *RPMLimiter) GetCurrentModelRPM(credentialName, modelName string) int {
	if r.getModelConfig(credentialName, modelName) == nil {
		return 0
	}
	return r.backend.currentRPM(modelCounterKey(credentialName, modelName))
}

// GetCurrentModelTPM returns the TPM for a (credential, model) pair.
func (r *RPMLimiter) GetCurrentModelTPM(credentialName, modelName string) int {
	if r.getModelConfig(credentialName, modelName) == nil {
		return 0
	}
	return r.backend.currentTPM(modelCounterKey(credentialName, modelName))
}

// TryAllowAll atomically checks credential RPM, credential TPM, model RPM, and model TPM.
// Records credential and model RPM if all checks pass. Returns true if allowed.
// modelName may be empty to skip model-level checks.
func (r *RPMLimiter) TryAllowAll(credentialName, modelName string) bool {
	credCfg := r.getCredentialConfig(credentialName)
	if credCfg == nil {
		return false
	}

	modelRPM := -1
	modelTPM := -1
	mKey := ""

	if modelName != "" {
		modCfg := r.getModelConfig(credentialName, modelName)
		if modCfg != nil {
			modelRPM = modCfg.rpm
			modelTPM = modCfg.tpm
			mKey = modelCounterKey(credentialName, modelName)
		}
	}

	return r.backend.tryAllowAll(
		credKey(credentialName), credCfg.rpm, credCfg.tpm,
		mKey, modelRPM, modelTPM,
	)
}

// SetCredentialCurrentUsage overwrites the sliding-window counters for a credential.
// Used to synchronize usage from remote proxies. No-op for Redis backend.
func (r *RPMLimiter) SetCredentialCurrentUsage(credentialName string, currentRPM, currentTPM int) {
	if r.getCredentialConfig(credentialName) == nil {
		return
	}
	r.backend.setCurrentUsage(credKey(credentialName), currentRPM, currentTPM)
}

// SetModelCurrentUsage overwrites the sliding-window counters for a (credential, model) pair.
// No-op for Redis backend.
func (r *RPMLimiter) SetModelCurrentUsage(credentialName, modelName string, currentRPM, currentTPM int) {
	if r.getModelConfig(credentialName, modelName) == nil {
		return
	}
	r.backend.setCurrentUsage(modelCounterKey(credentialName, modelName), currentRPM, currentTPM)
}

// GetLimitRPM returns the configured RPM limit for a credential (-1 = not tracked).
func (r *RPMLimiter) GetLimitRPM(credentialName string) int {
	cfg := r.getCredentialConfig(credentialName)
	if cfg == nil {
		return -1
	}
	return cfg.rpm
}

// GetLimitTPM returns the configured TPM limit for a credential (-1 = not tracked).
func (r *RPMLimiter) GetLimitTPM(credentialName string) int {
	cfg := r.getCredentialConfig(credentialName)
	if cfg == nil {
		return -1
	}
	return cfg.tpm
}

// GetModelLimitRPM returns the configured RPM limit for a model (-1 = not tracked).
func (r *RPMLimiter) GetModelLimitRPM(credentialName, modelName string) int {
	cfg := r.getModelConfig(credentialName, modelName)
	if cfg == nil {
		return -1
	}
	return cfg.rpm
}

// GetModelLimitTPM returns the configured TPM limit for a model (-1 = not tracked).
func (r *RPMLimiter) GetModelLimitTPM(credentialName, modelName string) int {
	cfg := r.getModelConfig(credentialName, modelName)
	if cfg == nil {
		return -1
	}
	return cfg.tpm
}

// GetAllModels returns all tracked "credential:model" keys.
func (r *RPMLimiter) GetAllModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.modelLimits))
	for k := range r.modelLimits {
		keys = append(keys, k)
	}
	return keys
}

// ModelPair represents a parsed credential:model pair.
type ModelPair struct {
	Credential string
	Model      string
}

// GetAllModelPairs returns all tracked (credential, model) pairs pre-parsed.
func (r *RPMLimiter) GetAllModelPairs() []ModelPair {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pairs := make([]ModelPair, 0, len(r.modelLimits))
	for key := range r.modelLimits {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 {
			pairs = append(pairs, ModelPair{Credential: parts[0], Model: parts[1]})
		}
	}
	return pairs
}
