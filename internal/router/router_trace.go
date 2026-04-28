package router

import "net/http"

func (r *Router) handleTrace(w http.ResponseWriter, req *http.Request) {
	r.proxy.HandleTrace(w, req)
}

func (r *Router) handleVisualTrace(w http.ResponseWriter, req *http.Request) {
	r.proxy.HandleVisualTrace(w, req)
}
