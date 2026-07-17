package proxy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/logger"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitializeShadowContextAcceptsSignedIdentityAndEchoesCallID(t *testing.T) {
	verifier, privateKey := proxyTestVerifier(t)
	token := proxySignContext(t, privateKey, "call-123", "jti-1")
	p := &Proxy{logger: slog.New(slog.DiscardHandler), shadowContextVerifier: verifier}
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req.Header.Set(shadowcontext.AuthContextHeader, token)
	req.Header.Set(shadowcontext.CallIDHeader, "call-123")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{RequestID: "air-event-1", Request: req}

	req = p.initializeShadowContext(w, req, logCtx)

	assert.Equal(t, "call-123", w.Header().Get(shadowcontext.CallIDHeader))
	assert.Equal(t, "call-123", req.Header.Get(shadowcontext.CallIDHeader))
	assert.Empty(t, req.Header.Get(shadowcontext.AuthContextHeader))
	assert.Equal(t, "call-123", logger.CallIDFromContext(req.Context()))
	assert.Equal(t, "call-123", logCtx.CallID)
	assert.Equal(t, shadowcontext.StateValid, logCtx.ShadowContext.State)
	assert.Equal(t, testClientKeyHash, logCtx.ShadowContext.Identity.APIKeyHash)
	assert.Equal(t, "public-model", logCtx.ShadowContext.Identity.PublicModel)
	assert.Equal(t, "deployment-1", logCtx.ShadowContext.Identity.DeploymentID)
}

func TestInitializeShadowContextGeneratesCallIDWhenContextMissing(t *testing.T) {
	p := &Proxy{logger: slog.New(slog.DiscardHandler)}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{RequestID: "air-event-1", Request: req}

	req = p.initializeShadowContext(w, req, logCtx)

	_, err := uuid.Parse(logCtx.CallID)
	require.NoError(t, err)
	assert.Equal(t, logCtx.CallID, w.Header().Get(shadowcontext.CallIDHeader))
	assert.Equal(t, shadowcontext.StateMissing, logCtx.ShadowContext.State)
	assert.Empty(t, logCtx.ShadowContext.Identity.APIKeyHash)
	assert.Equal(t, logCtx.CallID, logger.CallIDFromContext(req.Context()))
}

func TestInitializeShadowContextRejectsCallIDMismatch(t *testing.T) {
	verifier, privateKey := proxyTestVerifier(t)
	token := proxySignContext(t, privateKey, "signed-call", "jti-2")
	p := &Proxy{logger: slog.New(slog.DiscardHandler), shadowContextVerifier: verifier}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set(shadowcontext.AuthContextHeader, token)
	req.Header.Set(shadowcontext.CallIDHeader, "supplied-call")
	w := httptest.NewRecorder()
	logCtx := &RequestLogContext{RequestID: "air-event-1", Request: req}

	p.initializeShadowContext(w, req, logCtx)

	assert.Equal(t, "supplied-call", logCtx.CallID)
	assert.Equal(t, shadowcontext.StateInvalid, logCtx.ShadowContext.State)
	assert.ErrorIs(t, logCtx.ShadowContext.Err, shadowcontext.ErrCallIDMismatch)
	assert.Empty(t, logCtx.ShadowContext.Identity.APIKeyHash)
}

func proxyTestVerifier(t *testing.T) (*shadowcontext.Verifier, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	verifier, err := shadowcontext.NewVerifier(config.ShadowAuthContextConfig{
		Issuer:          "litellm",
		Audience:        "air-ru01",
		PublicKeys:      map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(publicKey)},
		ClockSkew:       30 * time.Second,
		ReplayCacheSize: 10,
	})
	require.NoError(t, err)
	return verifier, privateKey
}

func proxySignContext(t *testing.T, privateKey ed25519.PrivateKey, callID, jti string) string {
	t.Helper()
	now := time.Now()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "key-1"})
	require.NoError(t, err)
	payload, err := json.Marshal(shadowcontext.Claims{
		Issuer:         "litellm",
		Audience:       shadowcontext.Audience{"air-ru01"},
		IssuedAt:       now.Add(-time.Second).Unix(),
		ExpiresAt:      now.Add(time.Minute).Unix(),
		ID:             jti,
		APIKeyHash:     testClientKeyHash,
		UserID:         "user-1",
		TeamID:         "team-1",
		OrganizationID: "org-1",
		ProjectID:      "project-1",
		AgentID:        "agent-1",
		PublicModel:    "public-model",
		DeploymentID:   "deployment-1",
		EndUser:        "end-user-1",
		Tags:           []string{"shadow"},
		CallID:         callID,
	})
	require.NoError(t, err)
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := encodedHeader + "." + encodedPayload
	signature := ed25519.Sign(privateKey, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}
