// Package api implements the manager's REST + SSE surface (the Slice 1 subset of API.md).
// The dashboard and any other client consume this; the manager is the only thing that
// talks to associates.
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/thinkaliker/labassistant/manager/events"
	"github.com/thinkaliker/labassistant/manager/hub"
	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/state"
	"github.com/thinkaliker/labassistant/module"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// Deps are the subsystems the API handlers need.
type Deps struct {
	Store  *state.Store
	Jobs   *jobs.Registry
	Events *events.Broker
	Hub    *hub.Hub
}

// Router returns the /api/v1 handler.
func Router(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/overview", d.overview)
	mux.HandleFunc("GET /api/v1/hosts", d.listHosts)
	mux.HandleFunc("GET /api/v1/hosts/{id}", d.getHost)
	mux.HandleFunc("GET /api/v1/hosts/{id}/status", d.getHost)
	mux.HandleFunc("GET /api/v1/hosts/{id}/modules", d.getModules)
	mux.HandleFunc("POST /api/v1/hosts/{id}/modules/{name}/actions/{action}", d.runAction)
	mux.HandleFunc("GET /api/v1/jobs", d.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", d.getJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/events", d.jobEvents)
	mux.HandleFunc("GET /api/v1/events", d.events)
	return mux
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
	h, ok := d.Store.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "host not found")
		return
	}
	if !d.Hub.Connected(id) {
		writeErr(w, http.StatusConflict, "offline", "host is not connected")
		return
	}
	params, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "cannot read body")
		return
	}
	if len(params) == 0 {
		params = nil
	}

	job := d.Jobs.Create(id, name, action, params)
	cmd := &pb.Command{
		JobId:   job.ID,
		Module:  name,
		Action:  action,
		Params:  params,
		Timeout: durationpb.New(actionTimeout(h, name, action)),
	}
	if err := d.Hub.Dispatch(id, cmd); err != nil {
		d.Jobs.SetResult(job.ID, module.JobFailed, nil, err.Error())
		writeErr(w, http.StatusConflict, "dispatch_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"jobId": job.ID})
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

func actionTimeout(h state.Host, mod, action string) time.Duration {
	for _, m := range h.Modules {
		if m.Name != mod {
			continue
		}
		for _, a := range m.Actions {
			if a.Name == action && a.DefaultTimeout > 0 {
				return a.DefaultTimeout
			}
		}
	}
	return 5 * time.Minute
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
