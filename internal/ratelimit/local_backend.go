package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/utils"
)

// localCounter holds the in-process sliding-window state for one rate-limit key.
type localCounter struct {
	mu       sync.Mutex
	requests []time.Time
	tokens   []tokenUsage
}

// localBackend is the in-process counterBackend implementation.
// It replicates the original RPMLimiter sliding-window logic.
type localBackend struct {
	mu       sync.RWMutex
	counters map[string]*localCounter
}

func newLocalBackend() *localBackend {
	return &localBackend{
		counters: make(map[string]*localCounter),
	}
}

// getOrCreate returns the counter for key, creating it lazily if necessary.
func (b *localBackend) getOrCreate(key string) *localCounter {
	b.mu.RLock()
	c := b.counters[key]
	b.mu.RUnlock()
	if c != nil {
		return c
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	// Double-check after acquiring write lock.
	if c = b.counters[key]; c != nil {
		return c
	}
	c = &localCounter{
		requests: make([]time.Time, 0),
		tokens:   make([]tokenUsage, 0),
	}
	b.counters[key] = c
	return c
}

// --- RPM helpers (must be called with c.mu held) ---

func localCleanOldRequests(c *localCounter) int {
	now := utils.NowUTC()
	cutoff := now.Add(-time.Minute)
	valid := c.requests[:0]
	for _, t := range c.requests {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	c.requests = valid
	return len(valid)
}

func localRecordRequest(c *localCounter) {
	if len(c.requests) >= MaxRequestsBufferSize {
		localCleanOldRequests(c)
	}
	if len(c.requests) < MaxRequestsBufferSize {
		c.requests = append(c.requests, utils.NowUTC())
	}
}

func localCheckRPM(c *localCounter, limit int, record bool) bool {
	localCleanOldRequests(c)
	if limit != -1 && len(c.requests) >= limit {
		return false
	}
	if record {
		localRecordRequest(c)
	}
	return true
}

// --- TPM helpers (must be called with c.mu held) ---

func localCleanOldTokens(c *localCounter) int {
	now := utils.NowUTC()
	cutoff := now.Add(-time.Minute)
	valid := c.tokens[:0]
	total := 0
	for _, tu := range c.tokens {
		if tu.timestamp.After(cutoff) {
			valid = append(valid, tu)
			total += tu.count
		}
	}
	c.tokens = valid
	return total
}

func localCheckTPM(c *localCounter, limit int) bool {
	if limit == -1 {
		return true
	}
	return localCleanOldTokens(c) < limit
}

// --- counterBackend implementation ---

// Note: localBackend methods accept context but don't use it since
// in-memory operations are fast and not cancellable.

func (b *localBackend) tryAllowRPM(_ context.Context, key string, limit int) bool {
	c := b.getOrCreate(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	return localCheckRPM(c, limit, true)
}

func (b *localBackend) canAllowRPM(_ context.Context, key string, limit int) bool {
	c := b.getOrCreate(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	return localCheckRPM(c, limit, false)
}

func (b *localBackend) canAllowTPM(_ context.Context, key string, limit int) bool {
	c := b.getOrCreate(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	return localCheckTPM(c, limit)
}

func (b *localBackend) consumeTokens(_ context.Context, key string, tokenCount int) {
	c := b.getOrCreate(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tokens) >= MaxTokensBufferSize {
		localCleanOldTokens(c)
	}
	if len(c.tokens) < MaxTokensBufferSize {
		c.tokens = append(c.tokens, tokenUsage{timestamp: utils.NowUTC(), count: tokenCount})
	}
}

func (b *localBackend) currentRPM(_ context.Context, key string) int {
	c := b.getOrCreate(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	return localCleanOldRequests(c)
}

func (b *localBackend) currentTPM(_ context.Context, key string) int {
	c := b.getOrCreate(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	return localCleanOldTokens(c)
}

func (b *localBackend) tryAllowAll(_ context.Context, credKey string, credRPM, credTPM int, modelKey string, modelRPM, modelTPM int) bool {
	cred := b.getOrCreate(credKey)

	var mod *localCounter
	if modelKey != "" {
		mod = b.getOrCreate(modelKey)
	}

	// Consistent lock ordering: always lock credKey before modelKey.
	cred.mu.Lock()
	defer cred.mu.Unlock()

	if !localCheckRPM(cred, credRPM, false) {
		return false
	}
	if !localCheckTPM(cred, credTPM) {
		return false
	}

	if mod != nil {
		mod.mu.Lock()
		defer mod.mu.Unlock()

		if !localCheckRPM(mod, modelRPM, false) {
			return false
		}
		if !localCheckTPM(mod, modelTPM) {
			return false
		}
	}

	// All checks passed — record.
	localRecordRequest(cred)
	if mod != nil {
		localRecordRequest(mod)
	}
	return true
}

func (b *localBackend) setCurrentUsage(_ context.Context, key string, currentRPM, currentTPM int) {
	c := b.getOrCreate(key)
	c.mu.Lock()
	defer c.mu.Unlock()

	now := utils.NowUTC()
	windowStart := now.Add(-59 * time.Second)

	if currentRPM > 0 {
		c.requests = make([]time.Time, currentRPM)
		for i := range currentRPM {
			offset := time.Duration(int64(i)*59000/int64(currentRPM)) * time.Millisecond
			c.requests[i] = windowStart.Add(offset)
		}
	} else {
		c.requests = make([]time.Time, 0)
	}

	if currentTPM > 0 {
		const maxBuckets = 60
		numBuckets := currentTPM
		if numBuckets > maxBuckets {
			numBuckets = maxBuckets
		}
		c.tokens = make([]tokenUsage, numBuckets)
		tokensPerBucket := currentTPM / numBuckets
		remainder := currentTPM % numBuckets
		for i := range numBuckets {
			offset := time.Duration(int64(i)*59000/int64(numBuckets)) * time.Millisecond
			count := tokensPerBucket
			if i < remainder {
				count++
			}
			c.tokens[i] = tokenUsage{timestamp: windowStart.Add(offset), count: count}
		}
	} else {
		c.tokens = make([]tokenUsage, 0)
	}
}
