package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/thinkaliker/labassistant/manager/settings"
)

func (d Deps) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Settings.Manager())
}

func (d Deps) putSettings(w http.ResponseWriter, r *http.Request) {
	// Decode over the current settings so a client that omits a field (an older dashboard, a
	// script setting only the log level) leaves it as it was instead of zeroing it.
	m := d.Settings.Manager()
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	for name, v := range map[string]int{"heartbeatSeconds": m.HeartbeatSeconds, "statusPollSeconds": m.StatusPollSeconds} {
		if v < settings.MinIntervalSeconds || v > settings.MaxIntervalSeconds {
			writeErr(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("%s must be between %d and %d", name, settings.MinIntervalSeconds, settings.MaxIntervalSeconds))
			return
		}
	}
	if err := d.Settings.UpdateManager(m); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err.Error())
		return
	}
	d.Hub.SetIntervals(IntervalsOf(m))
	d.Aud.Record("settings_updated", "", "user", "manager settings updated", nil)
	writeJSON(w, http.StatusOK, d.Settings.Manager())
}

// IntervalsOf converts the stored refresh settings into the heartbeat and status-poll intervals
// the hub pushes to associates.
func IntervalsOf(m settings.Manager) (heartbeat, status time.Duration) {
	return time.Duration(m.HeartbeatSeconds) * time.Second, time.Duration(m.StatusPollSeconds) * time.Second
}

// refreshHosts asks every connected associate for a fresh heartbeat and module statuses now,
// rather than waiting for the next tick. Replies land on the event feed like any other update.
func (d Deps) refreshHosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]int{"requested": d.Hub.RefreshAll()})
}

// getModuleConfig returns the stored per-host module config plus its schema (from the
// module's advertised manifest), so a client can render a generic settings form.
func (d Deps) getModuleConfig(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	host, ok := d.Store.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "host not found")
		return
	}
	var schema json.RawMessage
	for _, m := range host.Modules {
		if m.Name == name {
			schema = m.ConfigSchema
		}
	}
	cfg := d.ModConfig.Get(id, name)
	if cfg == nil {
		cfg = json.RawMessage("{}")
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"config": cfg, "schema": schema})
}

func (d Deps) putModuleConfig(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "cannot read body")
		return
	}
	if err := d.ModConfig.Set(id, name, body); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err.Error())
		return
	}
	d.Aud.Record("module_config_updated", id, "user", "module config updated: "+name, nil)
	w.WriteHeader(http.StatusNoContent)
}
