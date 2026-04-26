package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	_ "embed"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/httputil"
)

//go:embed trace.html
var traceHTML string

const defaultTraceDepth = 25

// TraceCheck builds a recursive trace of this router and all reachable downstream proxy routers.
// depth controls how many hops to follow (0 = local only).
func (p *Proxy) TraceCheck(ctx context.Context, depth int) *httputil.ProxyTraceResponse {
	_, health := p.HealthCheck()

	status := "healthy"
	if health.CredentialsAvailable == 0 {
		status = "unhealthy"
	} else if health.CredentialsBanned > 0 {
		status = "degraded"
	}

	trace := &httputil.ProxyTraceResponse{
		RouterID:    p.routerID,
		Status:      status,
		Credentials: health.Credentials,
		Models:      health.Models,
	}

	if depth <= 0 {
		return trace
	}

	for _, cred := range p.balancer.GetCredentialsSnapshot() {
		if cred.Type != config.ProviderTypeProxy {
			continue
		}
		c := cred // copy for use in map key
		upstream, err := p.fetchUpstreamTrace(ctx, &c, depth-1)
		if trace.Upstreams == nil {
			trace.Upstreams = make(map[string]*httputil.ProxyTraceResponse)
		}
		if err != nil {
			trace.Upstreams[cred.Name] = &httputil.ProxyTraceResponse{FetchError: err.Error()}
		} else {
			trace.Upstreams[cred.Name] = upstream
		}
	}

	return trace
}

func (p *Proxy) fetchUpstreamTrace(ctx context.Context, cred *config.CredentialConfig, depth int) (*httputil.ProxyTraceResponse, error) {
	var result httputil.ProxyTraceResponse
	if err := httputil.FetchJSONFromProxy(ctx, cred, fmt.Sprintf("/trace?depth=%d", depth), p.logger, &result); err != nil {
		p.logger.Debug("upstream /trace unavailable, falling back to /health", "credential", cred.Name, "error", err)
		return p.fetchUpstreamHealth(ctx, cred)
	}
	return &result, nil
}

// fetchUpstreamHealth is a fallback for upstream routers that don't have /trace yet.
// It fetches /health and converts the result to a ProxyTraceResponse (no recursive upstreams).
func (p *Proxy) fetchUpstreamHealth(ctx context.Context, cred *config.CredentialConfig) (*httputil.ProxyTraceResponse, error) {
	var health httputil.ProxyHealthResponse
	if err := httputil.FetchJSONFromProxy(ctx, cred, "/health", p.logger, &health); err != nil {
		return nil, err
	}
	return &httputil.ProxyTraceResponse{
		RouterID:    cred.Name,
		Status:      health.Status,
		Credentials: health.Credentials,
		Models:      health.Models,
	}, nil
}

// HandleTrace returns the full trace tree as JSON.
func (p *Proxy) HandleTrace(w http.ResponseWriter, r *http.Request) {
	depth := defaultTraceDepth
	if d := r.URL.Query().Get("depth"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n >= 0 && n <= 10 {
			depth = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	trace := p.TraceCheck(ctx, depth)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(trace); err != nil {
		p.logger.Error("Failed to encode trace response", "error", err)
	}
}

// HandleVisualTrace serves the HTML trace dashboard.
func (p *Proxy) HandleVisualTrace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(traceHTML))
}
