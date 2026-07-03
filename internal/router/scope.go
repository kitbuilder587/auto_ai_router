package router

import (
	"net/http"

	"github.com/mixaill76/auto_ai_router/internal/proxy"
	"github.com/mixaill76/auto_ai_router/internal/scope"
)

func (r *Router) visibilityScope(w http.ResponseWriter, req *http.Request) (scope.Context, bool) {
	if r.proxy == nil {
		return scope.PublicContext(), true
	}
	visibility, err := r.proxy.ScopeContextForRequest(req)
	if err != nil {
		proxy.WriteErrorUnauthorized(w, "Invalid token")
		return scope.PublicContext(), false
	}
	return visibility, true
}
