package router

import (
	"net/http"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/proxy/webui"
)

type ConfigView struct {
	Version string
	Commit  string
	Config  *config.Config
}

func (r *Router) handleVisualConfig(w http.ResponseWriter, req *http.Request) {
	view := ConfigView{
		Version: r.proxy.GetVersion(),
		Commit:  r.proxy.GetCommit(),
		Config:  r.appConfig,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := webui.ConfigTemplate.Execute(w, view); err != nil {
		r.logger.Error("Failed to render config template", "error", err)
	}
}
