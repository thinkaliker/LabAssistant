package api

import (
	"encoding/json"
	"net/http"

	"github.com/thinkaliker/labassistant/manager/scheduler"
)

func (d Deps) listTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Scheduler.List())
}

func (d Deps) createTask(w http.ResponseWriter, r *http.Request) {
	var t scheduler.Task
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if msg, ok := d.validateTask(t); !ok {
		writeErr(w, http.StatusUnprocessableEntity, "rejected", msg)
		return
	}
	created, err := d.Scheduler.Create(t)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (d Deps) updateTask(w http.ResponseWriter, r *http.Request) {
	// Decode over the task as it stands rather than into a zero value, so fields the client
	// left out keep their current value instead of being cleared. A dashboard save used to
	// omit params and timezone, which silently widened a scoped task (e.g. "duo update of the
	// media stack") into a whole-host run and moved its fire time to the manager's zone.
	t, ok := d.Scheduler.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "task not found")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if msg, ok := d.validateTask(t); !ok {
		writeErr(w, http.StatusUnprocessableEntity, "rejected", msg)
		return
	}
	updated, err := d.Scheduler.Update(r.PathValue("id"), t)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (d Deps) deleteTask(w http.ResponseWriter, r *http.Request) {
	if !d.Scheduler.Delete(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "not_found", "task not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateTask rejects scheduling a destructive action unless the task opts in.
func (d Deps) validateTask(t scheduler.Task) (string, bool) {
	if t.Module == "" || t.Action == "" || len(t.HostIDs) == 0 {
		return "module, action, and at least one host are required", false
	}
	if !t.AllowDestructive {
		for _, h := range t.HostIDs {
			if d.Runner.IsDestructive(h, t.Module, t.Action) {
				return "action is destructive; set allowDestructive to schedule it", false
			}
		}
	}
	return "", true
}
