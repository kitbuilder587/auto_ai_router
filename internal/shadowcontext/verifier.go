// Package shadowcontext verifies the internal LiteLLM-to-AIR shadow identity
// envelope. The envelope is a compact JWS signed with Ed25519 (alg=EdDSA).
package shadowcontext

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
)

const (
	AuthContextHeader = "X-Vsellm-Auth-Context"
	CallIDHeader      = "X-Litellm-Call-Id"
)

var (
	ErrMalformed        = errors.New("shadow context: malformed JWS")
	ErrInvalidSignature = errors.New("shadow context: invalid signature")
	ErrExpired          = errors.New("shadow context: expired")
	ErrReplay           = errors.New("shadow context: replayed jti")
	ErrReplayCacheFull  = errors.New("shadow context: replay cache full")
	ErrInvalidClaims    = errors.New("shadow context: invalid claims")
	ErrCallIDMismatch   = errors.New("shadow context: call id mismatch")
)

type State string

const (
	StateMissing  State = "missing"
	StateValid    State = "valid"
	StateInvalid  State = "invalid"
	StateExpired  State = "expired"
	StateReplayed State = "replayed"
)

// Audience accepts both the JWT string form and the multi-audience array form.
type Audience []string

func (a *Audience) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = Audience{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return fmt.Errorf("aud must be string or string array: %w", err)
	}
	*a = Audience(multiple)
	return nil
}

// Claims is the signed transport contract produced by the future LiteLLM hook.
type Claims struct {
	Issuer    string   `json:"iss"`
	Audience  Audience `json:"aud"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	ID        string   `json:"jti"`

	APIKeyHash     string   `json:"api_key_hash"`
	UserID         string   `json:"user_id,omitempty"`
	TeamID         string   `json:"team_id,omitempty"`
	OrganizationID string   `json:"organization_id,omitempty"`
	ProjectID      string   `json:"project_id,omitempty"`
	AgentID        string   `json:"agent_id,omitempty"`
	PublicModel    string   `json:"public_model,omitempty"`
	DeploymentID   string   `json:"deployment_id,omitempty"`
	EndUser        string   `json:"end_user,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	// OriginalCallType preserves the user-facing operation when LiteLLM
	// translates it to another AIR route before forwarding the request.
	OriginalCallType string `json:"original_call_type"`
	CallID           string `json:"call_id"`
}

type Identity struct {
	APIKeyHash     string
	UserID         string
	TeamID         string
	OrganizationID string
	ProjectID      string
	AgentID        string
	PublicModel    string
	DeploymentID   string
	EndUser        string
	Tags           []string
	// OriginalCallType is populated only after the containing JWS is verified.
	OriginalCallType string
	CallID           string
}

type Result struct {
	State    State
	Identity Identity
	CallID   string
	Err      error
}

type jwsHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ,omitempty"`
	KeyID     string `json:"kid"`
}

type replayCache struct {
	mu         sync.Mutex
	maxSize    int
	entries    map[string]time.Time
	nextExpiry time.Time
}

type Verifier struct {
	issuer   string
	audience string
	keys     map[string]ed25519.PublicKey
	skew     time.Duration
	replay   *replayCache
	now      func() time.Time
}

func NewVerifier(cfg config.ShadowAuthContextConfig) (*Verifier, error) {
	keys := make(map[string]ed25519.PublicKey, len(cfg.PublicKeys))
	for kid, encoded := range cfg.PublicKeys {
		if strings.TrimSpace(kid) == "" {
			return nil, fmt.Errorf("shadow context: empty public key id")
		}
		key, err := decodePublicKey(encoded)
		if err != nil {
			return nil, fmt.Errorf("shadow context: decode public key %q: %w", kid, err)
		}
		keys[kid] = key
	}
	skew := cfg.ClockSkew
	if skew <= 0 {
		skew = 30 * time.Second
	}
	cacheSize := cfg.ReplayCacheSize
	if cacheSize <= 0 {
		cacheSize = 10000
	}
	return &Verifier{
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		keys:     keys,
		skew:     skew,
		replay: &replayCache{
			maxSize: cacheSize,
			entries: make(map[string]time.Time, cacheSize),
		},
		now: time.Now,
	}, nil
}

func (v *Verifier) Verify(compact string) Result {
	if strings.TrimSpace(compact) == "" {
		return Result{State: StateMissing}
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return invalidResult(ErrMalformed)
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return invalidResult(fmt.Errorf("%w: header encoding", ErrMalformed))
	}
	var header jwsHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return invalidResult(fmt.Errorf("%w: header JSON", ErrMalformed))
	}
	if header.Algorithm != "EdDSA" || (header.Type != "" && header.Type != "JWT") || header.KeyID == "" {
		return invalidResult(fmt.Errorf("%w: unsupported protected header", ErrMalformed))
	}
	key, ok := v.keys[header.KeyID]
	if !ok {
		return invalidResult(fmt.Errorf("%w: unknown kid", ErrInvalidSignature))
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return invalidResult(ErrInvalidSignature)
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return invalidResult(ErrInvalidSignature)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return invalidResult(fmt.Errorf("%w: payload encoding", ErrMalformed))
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return invalidResult(fmt.Errorf("%w: payload JSON", ErrMalformed))
	}
	if err := v.validateClaims(claims); err != nil {
		state := StateInvalid
		if errors.Is(err, ErrExpired) {
			state = StateExpired
		}
		return Result{State: state, Err: err}
	}

	now := v.now()
	if err := v.replay.accept(claims.Issuer+"\x00"+claims.ID, time.Unix(claims.ExpiresAt, 0).Add(v.skew), now); err != nil {
		if errors.Is(err, ErrReplay) {
			return Result{State: StateReplayed, Err: err}
		}
		return Result{State: StateInvalid, Err: err}
	}
	return Result{
		State: StateValid,
		Identity: Identity{
			APIKeyHash:       claims.APIKeyHash,
			UserID:           claims.UserID,
			TeamID:           claims.TeamID,
			OrganizationID:   claims.OrganizationID,
			ProjectID:        claims.ProjectID,
			AgentID:          claims.AgentID,
			PublicModel:      claims.PublicModel,
			DeploymentID:     claims.DeploymentID,
			EndUser:          claims.EndUser,
			Tags:             append([]string(nil), claims.Tags...),
			OriginalCallType: claims.OriginalCallType,
			CallID:           claims.CallID,
		},
	}
}

func (v *Verifier) validateClaims(claims Claims) error {
	now := v.now()
	if claims.Issuer == "" || claims.Issuer != v.issuer {
		return fmt.Errorf("%w: issuer", ErrInvalidClaims)
	}
	if !claims.Audience.contains(v.audience) || v.audience == "" {
		return fmt.Errorf("%w: audience", ErrInvalidClaims)
	}
	if claims.ID == "" || claims.IssuedAt == 0 || claims.ExpiresAt == 0 {
		return fmt.Errorf("%w: registered claims", ErrInvalidClaims)
	}
	if !validAPIKeyHash(claims.APIKeyHash) || !validCallID(claims.CallID) {
		return fmt.Errorf("%w: identity claims", ErrInvalidClaims)
	}
	if !validOriginalCallType(claims.OriginalCallType) {
		return fmt.Errorf("%w: original_call_type", ErrInvalidClaims)
	}
	if time.Unix(claims.ExpiresAt, 0).Before(now.Add(-v.skew)) {
		return ErrExpired
	}
	if claims.ExpiresAt <= claims.IssuedAt {
		return fmt.Errorf("%w: exp must be after iat", ErrInvalidClaims)
	}
	if time.Unix(claims.IssuedAt, 0).After(now.Add(v.skew)) {
		return fmt.Errorf("%w: iat is in the future", ErrInvalidClaims)
	}
	return nil
}

func validOriginalCallType(value string) bool {
	switch value {
	case "acompletion", "atext_completion", "aembedding", "aresponses", "aimage_generation", "aimage_edit":
		return true
	default:
		return false
	}
}

func validAPIKeyHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func Resolve(v *Verifier, compact, suppliedCallID string, generateCallID func() string) Result {
	var result Result
	if v == nil {
		if strings.TrimSpace(compact) == "" {
			result.State = StateMissing
		} else {
			result = invalidResult(fmt.Errorf("%w: verifier is not configured", ErrInvalidSignature))
		}
	} else {
		result = v.Verify(compact)
	}

	supplied := ""
	if validCallID(suppliedCallID) {
		supplied = suppliedCallID
	}
	if result.State == StateValid && supplied != "" && supplied != result.Identity.CallID {
		result = invalidResult(ErrCallIDMismatch)
	}

	switch {
	case supplied != "":
		result.CallID = supplied
	case result.State == StateValid && validCallID(result.Identity.CallID):
		result.CallID = result.Identity.CallID
	default:
		result.CallID = generateCallID()
	}
	return result
}

func (a Audience) contains(value string) bool {
	for _, candidate := range a {
		if candidate == value {
			return true
		}
	}
	return false
}

func invalidResult(err error) Result {
	return Result{State: StateInvalid, Err: err}
}

func validCallID(value string) bool {
	if len(value) == 0 || len(value) > 200 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func (c *replayCache) accept(key string, expiresAt, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if expiry, exists := c.entries[key]; exists && expiry.After(now) {
		return ErrReplay
	}

	if len(c.entries) >= c.maxSize && (c.nextExpiry.IsZero() || !c.nextExpiry.After(now)) {
		c.removeExpired(now)
	}
	if len(c.entries) >= c.maxSize {
		// Evicting a still-valid jti would make that token replayable. Reject the
		// new identity instead; proxy traffic remains fail-open and the row is
		// simply marked comparison-ineligible.
		return ErrReplayCacheFull
	}

	c.entries[key] = expiresAt
	if c.nextExpiry.IsZero() || expiresAt.Before(c.nextExpiry) {
		c.nextExpiry = expiresAt
	}
	return nil
}

func (c *replayCache) removeExpired(now time.Time) {
	c.nextExpiry = time.Time{}
	for key, expiry := range c.entries {
		if !expiry.After(now) {
			delete(c.entries, key)
			continue
		}
		if c.nextExpiry.IsZero() || expiry.Before(c.nextExpiry) {
			c.nextExpiry = expiry
		}
	}
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	encoded = strings.TrimSpace(encoded)
	if block, _ := pem.Decode([]byte(encoded)); block != nil {
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := parsed.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("public key is not Ed25519")
		}
		return key, nil
	}

	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.StdEncoding,
		base64.RawStdEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(encoded)
		if err == nil && len(decoded) == ed25519.PublicKeySize {
			return ed25519.PublicKey(decoded), nil
		}
	}
	return nil, fmt.Errorf("expected %d-byte base64 or PKIX PEM Ed25519 key", ed25519.PublicKeySize)
}
