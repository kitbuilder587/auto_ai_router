package router

import (
	"net/http"

	"github.com/mixaill76/auto_ai_router/internal/proxy"
)

func (r *Router) handleTrace(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("X-Router-Version", proxy.Version)
	w.Header().Set("X-Router-Commit", proxy.Commit)
	r.proxy.HandleTrace(w, req)
}

func (r *Router) handleVisualTrace(w http.ResponseWriter, req *http.Request) {
	r.proxy.HandleVisualTrace(w, req)
}
