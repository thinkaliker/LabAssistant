// Package api implements the manager's REST + SSE surface (the Slice 1 subset of API.md).
// The dashboard and any other client consume this; the manager is the only thing that
// talks to associates.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/thinkaliker/labassistant/manager/actions"
	"github.com/thinkaliker/labassistant/manager/auditor"
	"github.com/thinkaliker/labassistant/manager/events"
	"github.com/thinkaliker/labassistant/manager/hub"
	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/modconfig"
	"github.com/thinkaliker/labassistant/manager/quartermaster"
	"github.com/thinkaliker/labassistant/manager/scheduler"
	"github.com/thinkaliker/labassistant/manager/settings"
	"github.com/thinkaliker/labassistant/manager/state"
)

// Deps are the subsystems the API handlers need.
type Deps struct {
	Store     *state.Store
	Jobs      *jobs.Registry
	Events    *events.Broker
	Hub       *hub.Hub
	QM        *quartermaster.Quartermaster
	Runner    *actions.Runner
	Scheduler *scheduler.Scheduler
	Aud       *auditor.Auditor
	Settings  *settings.Store
	Sessions  *Sessions
	Backup    *Backup
	ModConfig *modconfig.Store
}

// Router returns the /api/v1 handler.
func Router(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/overview", d.overview)
	mux.HandleFunc("GET /api/v1/hosts", d.listHosts)
	mux.HandleFunc("POST /api/v1/hosts", d.addHost)
	mux.HandleFunc("GET /api/v1/hosts/{id}", d.getHost)
	mux.HandleFunc("PUT /api/v1/hosts/{id}", d.editHost)
	mux.HandleFunc("DELETE /api/v1/hosts/{id}", d.deleteHost)
	mux.HandleFunc("GET /api/v1/hosts/{id}/status", d.getHost)
	mux.HandleFunc("GET /api/v1/hosts/{id}/modules", d.getModules)
	mux.HandleFunc("POST /api/v1/hosts/{id}/modules/{name}/actions/{action}", d.runAction)
	mux.HandleFunc("GET /api/v1/services", d.services)
	mux.HandleFunc("GET /api/v1/hosts/{id}/logs", d.hostLogs)
	mux.HandleFunc("GET /api/v1/jobs", d.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", d.getJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/events", d.jobEvents)
	mux.HandleFunc("GET /api/v1/approvals", d.listApprovals)
	mux.HandleFunc("POST /api/v1/approvals/{id}/confirm", d.confirmApproval)
	mux.HandleFunc("POST /api/v1/approvals/{id}/reject", d.rejectApproval)
	mux.HandleFunc("GET /api/v1/tasks", d.listTasks)
	mux.HandleFunc("POST /api/v1/tasks", d.createTask)
	mux.HandleFunc("PUT /api/v1/tasks/{id}", d.updateTask)
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", d.deleteTask)
	mux.HandleFunc("GET /api/v1/audit", d.audit)
	mux.HandleFunc("GET /api/v1/events", d.events)

	mux.HandleFunc("POST /api/v1/auth/login", d.login)
	mux.HandleFunc("POST /api/v1/auth/logout", d.logout)
	mux.HandleFunc("GET /api/v1/auth/session", d.session)
	mux.HandleFunc("GET /api/v1/auth/tokens", d.listTokens)
	mux.HandleFunc("POST /api/v1/auth/tokens", d.createToken)
	mux.HandleFunc("DELETE /api/v1/auth/tokens/{id}", d.revokeToken)

	mux.HandleFunc("GET /api/v1/settings", d.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", d.putSettings)
	mux.HandleFunc("GET /api/v1/hosts/{id}/modules/{name}/config", d.getModuleConfig)
	mux.HandleFunc("PUT /api/v1/hosts/{id}/modules/{name}/config", d.putModuleConfig)
	mux.HandleFunc("GET /api/v1/backup", d.backup)
	mux.HandleFunc("POST /api/v1/restore", d.restore)

	return d.authMiddleware(mux)
}

func (d Deps) overview(w http.ResponseWriter, r *http.Request) {
	hosts := d.Store.Hosts()
	var online, offline, enrolling, errc int
	var cpuSum, memSum float64
	var healthN int
	var updates int
	for _, h := range hosts {
		switch h.Status {
		case state.StatusOnline:
			online++
		case state.StatusEnrolling:
			enrolling++
		case state.StatusError:
			errc++
		default:
			offline++
		}
		if h.Health != nil {
			cpuSum += h.Health.CPUPercent
			memSum += h.Health.MemPercent
			healthN++
		}
		updates += countUpdates(h)
	}
	resCPU, resMem := 0.0, 0.0
	if healthN > 0 {
		resCPU = round1(cpuSum / float64(healthN))
		resMem = round1(memSum / float64(healthN))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hosts": map[string]int{
			"total": len(hosts), "online": online, "offline": offline,
			"enrolling": enrolling, "error": errc,
		},
		"updates":   map[string]int{"packages": updates},
		"resources": map[string]float64{"cpuPercent": resCPU, "memPercent": resMem},
	})
}

func (d Deps) listHosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Store.Hosts())
}

func (d Deps) getHost(w http.ResponseWriter, r *http.Request) {
	h, ok := d.Store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "host not found")
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (d Deps) getModules(w http.ResponseWriter, r *http.Request) {
	h, ok := d.Store.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "host not found")
		return
	}
	writeJSON(w, http.StatusOK, h.Modules)
}

func (d Deps) runAction(w http.ResponseWriter, r *http.Request) {
	id, name, action := r.PathValue("id"), r.PathValue("name"), r.PathValue("action")
	params, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "cannot read body")
		return
	}
	if len(params) == 0 {
		params = nil
	}
	out, err := d.Runner.Run(id, name, action, params, false)
	if err != nil {
		writeRunnerErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (d Deps) listJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Jobs.List())
}

func (d Deps) getJob(w http.ResponseWriter, r *http.Request) {
	j, ok := d.Jobs.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	writeJSON(w, http.StatusOK, j.Snapshot())
}

// countUpdates sums a "count" field from any module status that reports one (e.g. qup).
func countUpdates(h state.Host) int {
	total := 0
	for _, m := range h.Modules {
		if len(m.Status) == 0 {
			continue
		}
		var s struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal(m.Status, &s); err == nil {
			total += s.Count
		}
	}
	return total
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"code": errCode, "message": msg}})
}

// writeRunnerErr maps actions.Runner errors to HTTP statuses.
func writeRunnerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, actions.ErrHostNotFound), errors.Is(err, actions.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, actions.ErrOffline):
		writeErr(w, http.StatusConflict, "offline", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "error", err.Error())
	}
}
