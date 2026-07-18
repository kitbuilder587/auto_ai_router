package proxy

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
)

type RouteKind string

const (
	RouteCompletion      RouteKind = "acompletion"
	RouteTextCompletion  RouteKind = "atext_completion"
	RouteEmbedding       RouteKind = "aembedding"
	RouteResponses       RouteKind = "aresponses"
	RouteImageGeneration RouteKind = "aimage_generation"
	RouteImageEdit       RouteKind = "aimage_edit"
	RouteUnknown         RouteKind = "unknown"
)

type BillingAttempt struct {
	Sequence      int    `json:"sequence"`
	Credential    string `json:"credential,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ProviderModel string `json:"provider_model,omitempty"`
	TargetHost    string `json:"target_host,omitempty"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
}

// BillingContext is updated value-by-value. Slice-bearing methods copy their
// input so retries and conversions cannot retroactively mutate prior snapshots.
type BillingContext struct {
	eventID            string
	callID             string
	originalEndpoint   string
	callType           RouteKind
	publicModel        string
	backendModel       string
	providerModel      string
	deploymentID       string
	provider           string
	credential         string
	targetHost         string
	providerResponseID string
	attempts           []BillingAttempt
}

const maxResponseIDCaptureBytes = 256 * 1024

// responseIDCapture joins arbitrarily split SSE reads until the first
// client-visible response ID can be decoded. Network reads are not event
// aligned, so parsing each chunk independently can miss an ID split in two.
type responseIDCapture struct {
	buffer []byte
	id     string
}

func (c *responseIDCapture) Observe(chunk []byte) string {
	if c.id != "" || len(chunk) == 0 {
		return c.id
	}
	if id := extractClientVisibleResponseID(chunk); id != "" {
		c.id = id
		c.buffer = nil
		return id
	}
	if len(c.buffer)+len(chunk) > maxResponseIDCaptureBytes {
		c.buffer = nil
		if len(chunk) > maxResponseIDCaptureBytes {
			chunk = chunk[len(chunk)-maxResponseIDCaptureBytes:]
		}
	}
	c.buffer = append(c.buffer, chunk...)
	if id := extractClientVisibleResponseID(c.buffer); id != "" {
		c.id = id
		c.buffer = nil
	}
	return c.id
}

func (logCtx *RequestLogContext) captureProviderResponseID(capture *responseIDCapture, chunk []byte) {
	if logCtx == nil {
		return
	}
	logCtx.Billing = logCtx.Billing.WithProviderResponseID(capture.Observe(chunk))
}

func NewBillingContext(eventID, callID, endpoint string, identity shadowcontext.Identity) BillingContext {
	callType := routeKindFromEndpoint(endpoint)
	// LiteLLM may translate legacy Completions into Chat Completions before AIR
	// sees the request. Identity only contains this override after JWS
	// verification; direct and invalidly signed requests remain route-derived.
	if signedCallType := routeKindFromSignedOriginalCallType(identity.OriginalCallType); signedCallType != RouteUnknown {
		callType = signedCallType
	}
	return BillingContext{
		eventID:          eventID,
		callID:           callID,
		originalEndpoint: endpoint,
		callType:         callType,
		publicModel:      identity.PublicModel,
		deploymentID:     identity.DeploymentID,
	}
}

// WithPublicModel records the client-visible model for direct AIR requests.
// A verified chained request initializes publicModel from its signed identity,
// which is authoritative and therefore cannot be overwritten here.
func (b BillingContext) WithPublicModel(model string) BillingContext {
	if b.publicModel == "" && model != "" {
		b.publicModel = model
	}
	return b
}

func (b BillingContext) WithRouting(backendModel, providerModel, provider, credential, target string) BillingContext {
	if b.backendModel == "" {
		b.backendModel = backendModel
	}
	b.providerModel = providerModel
	b.provider = provider
	b.credential = credential
	b.targetHost = targetHost(target)
	return b
}

func (b BillingContext) WithProviderResponseID(responseID string) BillingContext {
	if b.providerResponseID == "" && responseID != "" {
		b.providerResponseID = responseID
	}
	return b
}

func (b BillingContext) AddAttempt(attempt BillingAttempt) BillingContext {
	copyOfAttempts := make([]BillingAttempt, len(b.attempts), len(b.attempts)+1)
	copy(copyOfAttempts, b.attempts)
	if attempt.Sequence == 0 {
		attempt.Sequence = len(copyOfAttempts) + 1
	}
	if attempt.TargetHost != "" {
		attempt.TargetHost = targetHost(attempt.TargetHost)
	}
	b.attempts = append(copyOfAttempts, attempt)
	return b
}

func (b BillingContext) CompleteLastAttempt(httpStatus int, outcome string) BillingContext {
	if len(b.attempts) == 0 {
		return b
	}
	b.attempts = append([]BillingAttempt(nil), b.attempts...)
	last := &b.attempts[len(b.attempts)-1]
	last.HTTPStatus = httpStatus
	last.Outcome = outcome
	return b
}

func (b BillingContext) SpendRequestID() string {
	// LiteLLM uses the client-visible provider response ID for normal requests.
	// The writer retains eventID as a collision-safe fallback when a provider
	// reuses an ID for more than one logical effect.
	if b.providerResponseID != "" {
		return b.providerResponseID
	}
	if b.eventID != "" {
		return b.eventID
	}
	return b.callID
}

func (b BillingContext) EventID() string            { return b.eventID }
func (b BillingContext) CallID() string             { return b.callID }
func (b BillingContext) OriginalEndpoint() string   { return b.originalEndpoint }
func (b BillingContext) CallType() RouteKind        { return b.callType }
func (b BillingContext) PublicModel() string        { return b.publicModel }
func (b BillingContext) BackendModel() string       { return b.backendModel }
func (b BillingContext) ProviderModel() string      { return b.providerModel }
func (b BillingContext) DeploymentID() string       { return b.deploymentID }
func (b BillingContext) Provider() string           { return b.provider }
func (b BillingContext) Credential() string         { return b.credential }
func (b BillingContext) TargetHost() string         { return b.targetHost }
func (b BillingContext) ProviderResponseID() string { return b.providerResponseID }
func (b BillingContext) Attempts() []BillingAttempt {
	return append([]BillingAttempt(nil), b.attempts...)
}

func routeKindFromEndpoint(endpoint string) RouteKind {
	switch strings.TrimSuffix(endpoint, "/") {
	case "/v1/chat/completions":
		return RouteCompletion
	case "/v1/completions":
		return RouteTextCompletion
	case "/v1/embeddings":
		return RouteEmbedding
	case "/v1/responses":
		return RouteResponses
	case "/v1/images/generations":
		return RouteImageGeneration
	case "/v1/images/edits":
		return RouteImageEdit
	default:
		return RouteUnknown
	}
}

func routeKindFromSignedOriginalCallType(callType string) RouteKind {
	switch callType {
	case string(RouteCompletion):
		return RouteCompletion
	case string(RouteTextCompletion):
		return RouteTextCompletion
	case string(RouteEmbedding):
		return RouteEmbedding
	case string(RouteResponses):
		return RouteResponses
	case string(RouteImageGeneration):
		return RouteImageGeneration
	case string(RouteImageEdit):
		return RouteImageEdit
	default:
		return RouteUnknown
	}
}

func extractClientVisibleResponseID(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if id := responseIDFromJSON(body); id != "" {
		return id
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if id := responseIDFromJSON([]byte(payload)); id != "" {
			return id
		}
	}
	return ""
}

func responseIDFromJSON(body []byte) string {
	var envelope struct {
		ID       string `json:"id"`
		Response *struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	if envelope.ID != "" {
		return envelope.ID
	}
	if envelope.Response != nil {
		return envelope.Response.ID
	}
	return ""
}

func targetHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.Host == "" {
		parsed, err = url.Parse("//" + raw)
		if err != nil {
			return ""
		}
	}
	return parsed.Host
}
