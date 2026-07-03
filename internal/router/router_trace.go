package router

import (
	"net/http"
)

func (r *Router) handleTrace(w http.ResponseWriter, req *http.Request) {
	visibility, ok := r.visibilityScope(w, req)
	if !ok {
		return
	}
	w.Header().Set("X-Router-Version", r.proxy.GetVersion())
	w.Header().Set("X-Router-Commit", r.proxy.GetCommit())
	r.proxy.HandleTraceScoped(w, req, visibility)
}

func (r *Router) handleVisualTrace(w http.ResponseWriter, req *http.Request) {
	r.proxy.HandleVisualTrace(w, req)
}
