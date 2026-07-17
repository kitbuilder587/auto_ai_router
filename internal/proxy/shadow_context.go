package proxy

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/mixaill76/auto_ai_router/internal/logger"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func (p *Proxy) initializeShadowContext(
	w http.ResponseWriter,
	r *http.Request,
	logCtx *RequestLogContext,
) *http.Request {
	resolved := shadowcontext.Resolve(
		p.shadowContextVerifier,
		r.Header.Get(shadowcontext.AuthContextHeader),
		r.Header.Get(shadowcontext.CallIDHeader),
		func() string { return uuid.New().String() },
	)

	// The signed identity is AIR-internal and must never reach a provider. The
	// correlation ID is intentionally forwarded and echoed.
	r.Header.Del(shadowcontext.AuthContextHeader)
	r.Header.Set(shadowcontext.CallIDHeader, resolved.CallID)
	w.Header().Set(shadowcontext.CallIDHeader, resolved.CallID)
	r = r.WithContext(logger.WithCallID(r.Context(), resolved.CallID))

	logCtx.CallID = resolved.CallID
	logCtx.ShadowContext = resolved
	logCtx.Request = r

	if resolved.Err != nil {
		p.logger.WarnContext(r.Context(), "Rejected shadow auth context",
			"state", resolved.State,
			"error", resolved.Err,
		)
	}
	if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
		span.SetAttributes(
			attribute.String("litellm.call_id", resolved.CallID),
			attribute.String("aar.shadow_context_state", string(resolved.State)),
		)
	}
	return r
}
