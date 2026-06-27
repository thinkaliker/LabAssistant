package manager

import (
	"net/http"

	"github.com/thinkaliker/labassistant/dashboard"
	"github.com/thinkaliker/labassistant/manager/api"
)

// httpHandler mounts the REST/SSE API under /api/v1 and serves the embedded dashboard at
// the web root.
func (a *App) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", api.Router(api.Deps{
		Store:      a.store,
		Jobs:       a.jobs,
		Events:     a.events,
		Hub:        a.hub,
		QM:         a.qm,
		Runner:     a.runner,
		Scheduler:  a.scheduler,
		Aud:        a.aud,
		Settings:   a.settings,
		Sessions:   a.sessions,
		Backup:     a.backup,
		ModConfig:  a.modconfig,
		CA:         a.ca,
		RotateCert: a.rotateCert,
	}))
	mux.Handle("/", http.FileServerFS(dashboard.Assets))
	return mux
}
