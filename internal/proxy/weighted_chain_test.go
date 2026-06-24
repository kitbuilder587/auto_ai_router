package proxy

// Integration tests for the weighted load balancer across a real router chain:
//
//	client → router1 → (proxy credential) → router2 / terminal providers
//
// These cover that weighted round-robin behaves correctly end-to-end through proxy
// forwarding, that equal weights reproduce the historical round-robin split, and —
// critically — that weights compose per hop and are NOT multiplied across the chain.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// terminalServer is a leaf provider that counts how many requests it served.
func terminalServer(t *testing.T, counter *int32) *httptest.Server {
	t.Helper()
	return newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(counter, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
}

func proxyCred(name, baseURL string, weight int) config.CredentialConfig {
	return config.CredentialConfig{
		Name:    name,
		Type:    config.ProviderTypeProxy,
		BaseURL: baseURL,
		APIKey:  "master-key",
		RPM:     -1,
		TPM:     -1,
		Weight:  weight,
	}
}

func registerTestModel(prx *Proxy, credentialName, modelID string) {
	prx.rateLimiter.AddModelWithTPM(credentialName, modelID, -1, -1)
	prx.modelManager.AddModel(credentialName, modelID)
}

func serveProxyWithHealth(t *testing.T, prx *Proxy) *httptest.Server {
	t.Helper()
	return newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, health := prx.HealthCheck()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(health)
			return
		}
		prx.ProxyRequest(w, r)
	}))
}

// A router distributing across two proxy credentials honors their weights end-to-end:
// 3:1 weights yield an exact 3:1 split of forwarded requests (deterministic SWRR, no
// rate limiting or bans in play).
func TestWeightedChain_DistributionHonoredThroughProxy(t *testing.T) {
	var aCalls, bCalls int32
	provA := terminalServer(t, &aCalls)
	defer provA.Close()
	provB := terminalServer(t, &bCalls)
	defer provB.Close()

	router := NewTestProxyBuilder().
		WithCredentials(
			proxyCred("provA", provA.URL, 3),
			proxyCred("provB", provB.URL, 1),
		).
		WithRequestTimeout(5 * time.Second).
		Build()

	const cycles = 50
	for i := 0; i < 4*cycles; i++ {
		w := doChainRequest(router)
		require.Equal(t, http.StatusOK, w.Code, "request %d", i)
	}

	assert.Equal(t, int32(3*cycles), atomic.LoadInt32(&aCalls))
	assert.Equal(t, int32(1*cycles), atomic.LoadInt32(&bCalls))
}

// Equal weights through the chain reproduce the historical round-robin 50/50 split.
func TestWeightedChain_EqualWeightsRoundRobin(t *testing.T) {
	var aCalls, bCalls int32
	provA := terminalServer(t, &aCalls)
	defer provA.Close()
	provB := terminalServer(t, &bCalls)
	defer provB.Close()

	router := NewTestProxyBuilder().
		WithCredentials(
			proxyCred("provA", provA.URL, 1),
			proxyCred("provB", provB.URL, 1),
		).
		WithRequestTimeout(5 * time.Second).
		Build()

	for i := 0; i < 100; i++ {
		w := doChainRequest(router)
		require.Equal(t, http.StatusOK, w.Code, "request %d", i)
	}

	assert.Equal(t, int32(50), atomic.LoadInt32(&aCalls))
	assert.Equal(t, int32(50), atomic.LoadInt32(&bCalls))
}

// Weights compose per hop and are NOT multiplied across the chain. router1 forwards
// everything to router2 via a single high-weight credential (weight is irrelevant with
// one candidate); router2 splits across two providers 3:1. The end distribution must be
// router2's 3:1 — unaffected by router1's weight-100 credential.
func TestWeightedChain_PerHopNoWeightMultiplication(t *testing.T) {
	var aCalls, bCalls int32
	provA := terminalServer(t, &aCalls)
	defer provA.Close()
	provB := terminalServer(t, &bCalls)
	defer provB.Close()

	// router2: real Proxy splitting 3:1 across the two providers, served over HTTP.
	router2 := NewTestProxyBuilder().
		WithCredentials(
			proxyCred("provA", provA.URL, 3),
			proxyCred("provB", provB.URL, 1),
		).
		WithRequestTimeout(5 * time.Second).
		Build()
	router2Srv := newIPv4Server(t, http.HandlerFunc(router2.ProxyRequest))
	defer router2Srv.Close()

	// router1: single credential to router2 with an extreme weight that must not leak
	// into the downstream split.
	router1 := NewTestProxyBuilder().
		WithCredentials(proxyCred("router2", router2Srv.URL, 100)).
		WithRequestTimeout(5 * time.Second).
		Build()

	const cycles = 50
	for i := 0; i < 4*cycles; i++ {
		w := doChainRequest(router1)
		require.Equal(t, http.StatusOK, w.Code, "request %d", i)
	}

	assert.Equal(t, int32(3*cycles), atomic.LoadInt32(&aCalls), "downstream split must be router2's 3:1")
	assert.Equal(t, int32(1*cycles), atomic.LoadInt32(&bCalls))
}

// The first-line router must learn downstream model weights from /health and use them
// when choosing between downstream AirRouter instances. routerA exposes gpt-4 through
// two local providers with weights 20:1, while routerB exposes a single provider with
// weight 1. After syncing /health, root must route gpt-4 to routerA:routerB as 21:1,
// and routerA must still split its own traffic 20:1.
func TestWeightedChain_RootUsesDownstreamHealthWeights(t *testing.T) {
	var heavyCalls, lightCalls, routerBCalls int32
	heavyProvider := terminalServer(t, &heavyCalls)
	defer heavyProvider.Close()
	lightProvider := terminalServer(t, &lightCalls)
	defer lightProvider.Close()
	routerBProvider := terminalServer(t, &routerBCalls)
	defer routerBProvider.Close()

	routerA := NewTestProxyBuilder().
		WithCredentials(
			proxyCred("routerA-heavy", heavyProvider.URL, 20),
			proxyCred("routerA-light", lightProvider.URL, 1),
		).
		WithRequestTimeout(5 * time.Second).
		Build()
	registerTestModel(routerA, "routerA-heavy", "gpt-4")
	registerTestModel(routerA, "routerA-light", "gpt-4")
	routerASrv := serveProxyWithHealth(t, routerA)
	defer routerASrv.Close()

	routerB := NewTestProxyBuilder().
		WithCredentials(proxyCred("routerB-only", routerBProvider.URL, 1)).
		WithRequestTimeout(5 * time.Second).
		Build()
	registerTestModel(routerB, "routerB-only", "gpt-4")
	routerBSrv := serveProxyWithHealth(t, routerB)
	defer routerBSrv.Close()

	rootCredA := proxyCred("routerA", routerASrv.URL, 1)
	rootCredB := proxyCred("routerB", routerBSrv.URL, 1)
	root := NewTestProxyBuilder().
		WithCredentials(rootCredA, rootCredB).
		WithRequestTimeout(5 * time.Second).
		Build()

	UpdateStatsFromRemoteProxy(context.Background(), &rootCredA, root.rateLimiter, root.logger, root.modelManager)
	UpdateStatsFromRemoteProxy(context.Background(), &rootCredB, root.rateLimiter, root.logger, root.modelManager)

	require.Equal(t, 21, root.modelManager.GetModelWeightForCredential("gpt-4", "routerA"))
	require.Equal(t, 1, root.modelManager.GetModelWeightForCredential("gpt-4", "routerB"))

	const cycles = 5
	for i := 0; i < 22*cycles; i++ {
		w := doChainRequest(root)
		require.Equal(t, http.StatusOK, w.Code, "request %d", i)
	}

	assert.Equal(t, int32(20*cycles), atomic.LoadInt32(&heavyCalls))
	assert.Equal(t, int32(1*cycles), atomic.LoadInt32(&lightCalls))
	assert.Equal(t, int32(1*cycles), atomic.LoadInt32(&routerBCalls))
}
