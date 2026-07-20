package shadowcontext

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifierAcceptsValidEd25519JWS(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()
	verifier := newTestVerifier(t, publicKey, now)
	claims := validClaims(now)
	token := signJWS(t, privateKey, "key-1", claims)

	result := verifier.Verify(token)

	require.NoError(t, result.Err)
	assert.Equal(t, StateValid, result.State)
	assert.Equal(t, "cc557cce629a1cb98664b98a3d5f5600a90a91c5955c4fdddfa4d13c94bfdcd6", result.Identity.APIKeyHash)
	assert.Equal(t, "user-1", result.Identity.UserID)
	assert.Equal(t, "team-1", result.Identity.TeamID)
	assert.Equal(t, "org-1", result.Identity.OrganizationID)
	assert.Equal(t, "project-1", result.Identity.ProjectID)
	assert.Equal(t, "agent-1", result.Identity.AgentID)
	assert.Equal(t, "public-gpt", result.Identity.PublicModel)
	assert.Equal(t, "deployment-1", result.Identity.DeploymentID)
	assert.Equal(t, "end-user-1", result.Identity.EndUser)
	assert.Equal(t, []string{"golden", "shadow"}, result.Identity.Tags)
	assert.Equal(t, "call-1", result.Identity.CallID)
}

func TestVerifierRequiresCanonicalOriginalCallType(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()

	for index, callType := range []string{
		"acompletion",
		"atext_completion",
		"aembedding",
		"aresponses",
		"aimage_generation",
		"aimage_edit",
	} {
		t.Run(callType, func(t *testing.T) {
			verifier := newTestVerifier(t, publicKey, now)
			claims := validClaims(now)
			claims.ID = fmt.Sprintf("jti-%d", index)
			claims.OriginalCallType = callType

			result := verifier.Verify(signJWS(t, privateKey, "key-1", claims))

			require.NoError(t, result.Err)
			assert.Equal(t, StateValid, result.State)
			assert.Equal(t, callType, result.Identity.OriginalCallType)
		})
	}
}

func TestVerifierRejectsOlderTokenWithoutOriginalCallType(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()
	verifier := newTestVerifier(t, publicKey, now)
	claims := validClaims(now)
	claims.OriginalCallType = ""

	result := verifier.Verify(signJWS(t, privateKey, "key-1", claims))

	assert.ErrorIs(t, result.Err, ErrInvalidClaims)
	assert.Equal(t, StateInvalid, result.State)
	assert.Empty(t, result.Identity.OriginalCallType)
}

func TestVerifierRejectsUnsupportedOriginalCallType(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()
	verifier := newTestVerifier(t, publicKey, now)
	claims := validClaims(now)
	claims.OriginalCallType = "completion"

	result := verifier.Verify(signJWS(t, privateKey, "key-1", claims))

	assert.ErrorIs(t, result.Err, ErrInvalidClaims)
	assert.Equal(t, StateInvalid, result.State)
	assert.Empty(t, result.Identity.OriginalCallType)
}

func TestVerifierRejectsExpiredForgedAndReplayedJWS(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, attackerKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()

	t.Run("expired", func(t *testing.T) {
		verifier := newTestVerifier(t, publicKey, now)
		claims := validClaims(now)
		claims.ExpiresAt = now.Add(-time.Minute).Unix()
		result := verifier.Verify(signJWS(t, privateKey, "key-1", claims))
		assert.ErrorIs(t, result.Err, ErrExpired)
		assert.Equal(t, StateExpired, result.State)
	})

	t.Run("forged", func(t *testing.T) {
		verifier := newTestVerifier(t, publicKey, now)
		result := verifier.Verify(signJWS(t, attackerKey, "key-1", validClaims(now)))
		assert.ErrorIs(t, result.Err, ErrInvalidSignature)
		assert.Equal(t, StateInvalid, result.State)
	})

	t.Run("replayed", func(t *testing.T) {
		verifier := newTestVerifier(t, publicKey, now)
		token := signJWS(t, privateKey, "key-1", validClaims(now))
		first := verifier.Verify(token)
		require.NoError(t, first.Err)
		second := verifier.Verify(token)
		assert.ErrorIs(t, second.Err, ErrReplay)
		assert.Equal(t, StateReplayed, second.State)
	})
}

func TestVerifierNeverEvictsLiveJTIWhenReplayCacheIsFull(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()
	verifier := newTestVerifier(t, publicKey, now)
	verifier.replay.maxSize = 1

	firstClaims := validClaims(now)
	first := signJWS(t, privateKey, "key-1", firstClaims)
	require.Equal(t, StateValid, verifier.Verify(first).State)

	secondClaims := validClaims(now)
	secondClaims.ID = "jti-2"
	secondClaims.CallID = "call-2"
	second := verifier.Verify(signJWS(t, privateKey, "key-1", secondClaims))
	assert.Equal(t, StateInvalid, second.State)
	assert.ErrorIs(t, second.Err, ErrReplayCacheFull)

	replayed := verifier.Verify(first)
	assert.Equal(t, StateReplayed, replayed.State)
	assert.ErrorIs(t, replayed.Err, ErrReplay)
}

func TestVerifierRejectsWrongAlgorithmIssuerAudienceAndMissingClaims(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()

	tests := []struct {
		name   string
		header map[string]string
		mutate func(*Claims)
	}{
		{name: "algorithm", header: map[string]string{"alg": "HS256", "typ": "JWT", "kid": "key-1"}},
		{name: "issuer", mutate: func(c *Claims) { c.Issuer = "attacker" }},
		{name: "audience", mutate: func(c *Claims) { c.Audience = Audience{"someone-else"} }},
		{name: "jti", mutate: func(c *Claims) { c.ID = "" }},
		{name: "call id", mutate: func(c *Claims) { c.CallID = "" }},
		{name: "api key hash", mutate: func(c *Claims) { c.APIKeyHash = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier := newTestVerifier(t, publicKey, now)
			claims := validClaims(now)
			if tt.mutate != nil {
				tt.mutate(&claims)
			}
			header := tt.header
			if header == nil {
				header = map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "key-1"}
			}
			result := verifier.Verify(signJWSWithHeader(t, privateKey, header, claims))
			assert.Error(t, result.Err)
			assert.Equal(t, StateInvalid, result.State)
		})
	}
}

func TestVerifierRejectsMalformedCompactJWS(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	verifier := newTestVerifier(t, publicKey, time.Unix(1_800_000_000, 0).UTC())

	for _, token := range []string{"not-a-jws", "a.b", "%%%.e30.signature", "a..c"} {
		result := verifier.Verify(token)
		assert.Equal(t, StateInvalid, result.State, token)
		assert.Error(t, result.Err, token)
		assert.Empty(t, result.Identity.APIKeyHash, token)
	}
}

func TestResolveUsesSignedOrSuppliedCallIDAndRejectsMismatch(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()
	claims := validClaims(now)

	t.Run("signed call id when header missing", func(t *testing.T) {
		verifier := newTestVerifier(t, publicKey, now)
		resolved := Resolve(verifier, signJWS(t, privateKey, "key-1", claims), "", func() string { return "generated" })
		assert.Equal(t, "call-1", resolved.CallID)
		assert.Equal(t, StateValid, resolved.State)
	})

	t.Run("matching supplied call id", func(t *testing.T) {
		verifier := newTestVerifier(t, publicKey, now)
		resolved := Resolve(verifier, signJWS(t, privateKey, "key-1", claims), "call-1", func() string { return "generated" })
		assert.Equal(t, "call-1", resolved.CallID)
		assert.Equal(t, StateValid, resolved.State)
	})

	t.Run("mismatch rejects identity but keeps supplied correlation", func(t *testing.T) {
		verifier := newTestVerifier(t, publicKey, now)
		resolved := Resolve(verifier, signJWS(t, privateKey, "key-1", claims), "different-call", func() string { return "generated" })
		assert.Equal(t, "different-call", resolved.CallID)
		assert.Equal(t, StateInvalid, resolved.State)
		assert.Empty(t, resolved.Identity.APIKeyHash)
		assert.ErrorIs(t, resolved.Err, ErrCallIDMismatch)
	})

	t.Run("missing context generates id", func(t *testing.T) {
		verifier := newTestVerifier(t, publicKey, now)
		resolved := Resolve(verifier, "", "", func() string { return "generated" })
		assert.Equal(t, "generated", resolved.CallID)
		assert.Equal(t, StateMissing, resolved.State)
	})
}

func newTestVerifier(t *testing.T, publicKey ed25519.PublicKey, now time.Time) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(config.SignedAuthContextConfig{
		Issuer:          "litellm",
		Audience:        "air-ru01",
		PublicKeys:      map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(publicKey)},
		ClockSkew:       30 * time.Second,
		ReplayCacheSize: 10,
	})
	require.NoError(t, err)
	verifier.now = func() time.Time { return now }
	return verifier
}

func validClaims(now time.Time) Claims {
	return Claims{
		Issuer:           "litellm",
		Audience:         Audience{"air-ru01"},
		IssuedAt:         now.Add(-time.Second).Unix(),
		ExpiresAt:        now.Add(time.Minute).Unix(),
		ID:               "jti-1",
		APIKeyHash:       "cc557cce629a1cb98664b98a3d5f5600a90a91c5955c4fdddfa4d13c94bfdcd6",
		UserID:           "user-1",
		TeamID:           "team-1",
		OrganizationID:   "org-1",
		ProjectID:        "project-1",
		AgentID:          "agent-1",
		PublicModel:      "public-gpt",
		DeploymentID:     "deployment-1",
		EndUser:          "end-user-1",
		Tags:             []string{"golden", "shadow"},
		OriginalCallType: "acompletion",
		CallID:           "call-1",
	}
}

func signJWS(t *testing.T, privateKey ed25519.PrivateKey, kid string, claims Claims) string {
	t.Helper()
	return signJWSWithHeader(t, privateKey, map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": kid}, claims)
}

func signJWSWithHeader(t *testing.T, privateKey ed25519.PrivateKey, header map[string]string, claims Claims) string {
	t.Helper()
	headerJSON, err := json.Marshal(header)
	require.NoError(t, err)
	payloadJSON, err := json.Marshal(claims)
	require.NoError(t, err)
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := encodedHeader + "." + encodedPayload
	signature := ed25519.Sign(privateKey, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}
