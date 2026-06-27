package api

import (
	"encoding/json"
	"net/http"
)

// svcStack mirrors duo's status payload, with the owning host attached.
type svcStack struct {
	HostID   string       `json:"hostId"`
	HostName string       `json:"hostName"`
	Name     string       `json:"name"`
	Path     string       `json:"path"`
	Status   string       `json:"status"`
	Services []svcService `json:"services"`
}

type svcService struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	Image           string `json:"image"`
	UpdateAvailable bool   `json:"updateAvailable"`
	HasLogs         bool   `json:"hasLogs"`
}

// duoStatus is the shape duo reports in its module status.
type duoStatus struct {
	Stacks []struct {
		Name     string       `json:"name"`
		Path     string       `json:"path"`
		Status   string       `json:"status"`
		Services []svcService `json:"services"`
	} `json:"stacks"`
}

// services is a read-only projection over the duo module across all hosts.
func (d Deps) services(w http.ResponseWriter, r *http.Request) {
	out := []svcStack{}
	for _, h := range d.Store.Hosts() {
		for _, m := range h.Modules {
			if m.Name != "duo" || len(m.Status) == 0 {
				continue
			}
			var ds duoStatus
			if err := json.Unmarshal(m.Status, &ds); err != nil {
				continue
			}
			for _, s := range ds.Stacks {
				out = append(out, svcStack{
					HostID: h.ID, HostName: h.Name, Name: s.Name, Path: s.Path,
					Status: s.Status, Services: s.Services,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stacks": out})
}
