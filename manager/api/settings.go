package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/thinkaliker/labassistant/manager/settings"
)

func (d Deps) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Settings.Manager())
}

func (d Deps) putSettings(w http.ResponseWriter, r *http.Request) {
	var m settings.Manager
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if err := d.Settings.UpdateManager(m); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err.Error())
		return
	}
	d.Aud.Record("settings_updated", "", "user", "manager settings updated", nil)
	writeJSON(w, http.StatusOK, d.Settings.Manager())
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
