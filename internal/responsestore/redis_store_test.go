package responsestore

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/converter/responses"
	"github.com/mixaill76/auto_ai_router/internal/ratelimit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redisStoreForTest creates a Redis-backed Store using VALKEY_ADDR env var.
// Skips the test if the variable is not set.
func redisStoreForTest(t *testing.T, keyPrefix string) Store {
	t.Helper()
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		t.Skip("VALKEY_ADDR not set, skipping Redis integration test")
	}
	client, err := ratelimit.NewValkeyClient(config.RedisConfig{
		InitAddresses:     []string{addr},
		ForceSingleClient: true,
	})
	require.NoError(t, err)

	store := NewRedis(client, keyPrefix)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// ── CleanupExpired no-op ────────────────────────────────────────────────────

func TestRedisStore_CleanupExpired_IsNoOp(t *testing.T) {
	// CleanupExpired is always nil for redis backend — test without real connection.
	s := &redisStore{}
	err := s.CleanupExpired(context.Background())
	assert.NoError(t, err)
}

// ── Close no-op ─────────────────────────────────────────────────────────────

func TestRedisStore_Close_IsNoOp(t *testing.T) {
	// Close should return nil without touching the shared client.
	s := &redisStore{}
	assert.NoError(t, s.Close())
}

// ── responseKey ─────────────────────────────────────────────────────────────

func TestRedisStore_ResponseKey(t *testing.T) {
	s := &redisStore{keyPrefix: "rl:"}
	assert.Equal(t, "rl:response:abc123", s.responseKey("abc123"))
}

func TestRedisStore_ResponseKey_EmptyPrefix(t *testing.T) {
	s := &redisStore{keyPrefix: ""}
	assert.Equal(t, "response:abc123", s.responseKey("abc123"))
}

// ── SaveResponse nil guard ──────────────────────────────────────────────────

func TestRedisStore_SaveResponse_NilResponse(t *testing.T) {
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		// test nil guard without a real connection — the nil check is before any I/O
		s := &redisStore{}
		err := s.SaveResponse(context.Background(), "hash", nil, nil, 0, nil, "cred-1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "nil")
		return
	}
	store := redisStoreForTest(t, "test:nil:")
	err := store.SaveResponse(context.Background(), "hash", nil, nil, 0, nil, "cred-1")
	assert.Error(t, err)
}

// ── integration tests (require VALKEY_ADDR) ─────────────────────────────────

func TestRedisStore_Integration_SaveAndGetResponse(t *testing.T) {
	store := redisStoreForTest(t, "test:sg1:")
	ctx := context.Background()

	resp := createTestResponse("redis-sg-1")
	err := store.SaveResponse(ctx, "hash-sg1", resp, nil, 0, nil, "cred-1")
	require.NoError(t, err)

	got, err := store.GetResponse(ctx, "redis-sg-1", "hash-sg1")
	require.NoError(t, err)
	assert.Equal(t, resp.ID, got.ID)
	assert.Equal(t, resp.Model, got.Model)
}

func TestRedisStore_Integration_GetResponse_NotFound(t *testing.T) {
	store := redisStoreForTest(t, "test:nf1:")
	ctx := context.Background()

	_, err := store.GetResponse(ctx, "nonexistent-id", "any-hash")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRedisStore_Integration_GetResponse_Unauthorized(t *testing.T) {
	store := redisStoreForTest(t, "test:auth1:")
	ctx := context.Background()

	resp := createTestResponse("redis-auth-1")
	require.NoError(t, store.SaveResponse(ctx, "correct-hash", resp, nil, 0, nil, "cred-1"))

	_, err := store.GetResponse(ctx, "redis-auth-1", "wrong-hash")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unauthorized")
}

func TestRedisStore_Integration_GetResponseByID(t *testing.T) {
	store := redisStoreForTest(t, "test:byid1:")
	ctx := context.Background()

	resp := createTestResponse("redis-byid-1")
	require.NoError(t, store.SaveResponse(ctx, "some-hash", resp, nil, 0, nil, "cred-1"))

	got, err := store.GetResponseByID(ctx, "redis-byid-1")
	require.NoError(t, err)
	assert.Equal(t, "redis-byid-1", got.ID)
}

func TestRedisStore_Integration_GetResponseByID_NotFound(t *testing.T) {
	store := redisStoreForTest(t, "test:byid2:")
	ctx := context.Background()

	_, err := store.GetResponseByID(ctx, "never-saved")
	assert.Error(t, err)
}

func TestRedisStore_Integration_GetEntry_WithMetadata(t *testing.T) {
	store := redisStoreForTest(t, "test:entry1:")
	ctx := context.Background()

	resp := createTestResponse("redis-entry-1")
	meta := map[string]string{"key": "value", "env": "test"}
	accInput := json.RawMessage(`{"role":"user","content":"hello"}`)

	require.NoError(t, store.SaveResponse(ctx, "entry-hash", resp, meta, 0, accInput, "cred-1"))

	entry, err := store.GetEntry(ctx, "redis-entry-1", "entry-hash")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "redis-entry-1", entry.ResponseID)
	assert.Equal(t, "entry-hash", entry.APIKeyHash)
	assert.Equal(t, "cred-1", entry.CredentialName)
	assert.Equal(t, "value", entry.Metadata["key"])
	assert.Equal(t, "test", entry.Metadata["env"])
	assert.NotEmpty(t, entry.AccumulatedInput)
}

func TestRedisStore_Integration_GetEntry_Unauthorized(t *testing.T) {
	store := redisStoreForTest(t, "test:entry2:")
	ctx := context.Background()

	resp := createTestResponse("redis-entry-auth")
	require.NoError(t, store.SaveResponse(ctx, "owner-hash", resp, nil, 0, nil, "cred-1"))

	_, err := store.GetEntry(ctx, "redis-entry-auth", "other-hash")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unauthorized")
}

func TestRedisStore_Integration_TTL_Expired(t *testing.T) {
	store := redisStoreForTest(t, "test:ttl1:")
	ctx := context.Background()

	resp := createTestResponse("redis-ttl-1")
	// TTL=1 second — Redis will expire the key
	require.NoError(t, store.SaveResponse(ctx, "ttl-hash", resp, nil, 1, nil, "cred-1"))

	// should be readable immediately
	got, err := store.GetResponse(ctx, "redis-ttl-1", "ttl-hash")
	require.NoError(t, err)
	assert.Equal(t, resp.ID, got.ID)

	// wait for Redis TTL + safety margin
	time.Sleep(1500 * time.Millisecond)

	_, err = store.GetResponse(ctx, "redis-ttl-1", "ttl-hash")
	assert.Error(t, err, "should be expired or not found after TTL")
}

func TestRedisStore_Integration_TTL_NotExpired(t *testing.T) {
	store := redisStoreForTest(t, "test:ttl2:")
	ctx := context.Background()

	resp := createTestResponse("redis-ttl-2")
	// TTL=30 seconds — should not expire during test
	require.NoError(t, store.SaveResponse(ctx, "ttl-hash2", resp, nil, 30, nil, "cred-1"))

	got, err := store.GetResponse(ctx, "redis-ttl-2", "ttl-hash2")
	require.NoError(t, err)
	assert.Equal(t, resp.ID, got.ID)
}

func TestRedisStore_Integration_NoTTL(t *testing.T) {
	store := redisStoreForTest(t, "test:nottl:")
	ctx := context.Background()

	resp := createTestResponse("redis-no-ttl")
	require.NoError(t, store.SaveResponse(ctx, "no-ttl-hash", resp, nil, 0, nil, "cred-1"))

	got, err := store.GetResponse(ctx, "redis-no-ttl", "no-ttl-hash")
	require.NoError(t, err)
	assert.Equal(t, resp.ID, got.ID)
}

func TestRedisStore_Integration_MultipleResponses(t *testing.T) {
	store := redisStoreForTest(t, "test:multi1:")
	ctx := context.Background()
	hash := "multi-hash"

	ids := []string{"r1", "r2", "r3", "r4", "r5"}
	for _, id := range ids {
		resp := createTestResponse(id)
		require.NoError(t, store.SaveResponse(ctx, hash, resp, nil, 0, nil, "cred-1"))
	}

	for _, id := range ids {
		got, err := store.GetResponse(ctx, id, hash)
		require.NoError(t, err)
		assert.Equal(t, id, got.ID)
	}
}

func TestRedisStore_Integration_Overwrite(t *testing.T) {
	store := redisStoreForTest(t, "test:overwrite:")
	ctx := context.Background()

	// Save v1
	resp1 := &responses.Response{
		ID:        "redis-overwrite",
		Model:     "gpt-3.5-turbo",
		CreatedAt: time.Now().Unix(),
		Status:    "completed",
	}
	require.NoError(t, store.SaveResponse(ctx, "hash", resp1, nil, 0, nil, "cred-1"))

	// Overwrite with v2
	resp2 := &responses.Response{
		ID:        "redis-overwrite",
		Model:     "gpt-4",
		CreatedAt: time.Now().Unix(),
		Status:    "completed",
	}
	require.NoError(t, store.SaveResponse(ctx, "hash", resp2, nil, 0, nil, "cred-2"))

	got, err := store.GetResponseByID(ctx, "redis-overwrite")
	require.NoError(t, err)
	assert.Equal(t, "gpt-4", got.Model, "should have overwritten with v2")
}

func TestRedisStore_Integration_CleanupExpired_IsNoOp(t *testing.T) {
	store := redisStoreForTest(t, "test:cleanup:")
	ctx := context.Background()

	// CleanupExpired is a no-op for Redis — should succeed regardless
	err := store.CleanupExpired(ctx)
	assert.NoError(t, err)
}
