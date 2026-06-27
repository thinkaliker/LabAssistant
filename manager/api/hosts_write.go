package api

import (
	"encoding/json"
	"net/http"

	"github.com/thinkaliker/labassistant/manager/quartermaster"
	"github.com/thinkaliker/labassistant/manager/state"
)

// addHostRequest is the POST /hosts body. ssh_password is transient (used only for the
// SSH bootstrap) and is never persisted.
type addHostRequest struct {
	Name        string `json:"name"`
	IP          string `json:"ip"`
	SSHUser     string `json:"sshUser"`
	SSHPassword string `json:"sshPassword"`
	Tailscale   bool   `json:"tailscale"`
}

// addHost starts async enrollment and returns the enrolling host plus its job id.
func (d Deps) addHost(w http.ResponseWriter, r *http.Request) {
	var req addHostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if req.Name == "" || req.IP == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "name and ip are required")
		return
	}
	hostID, jobID, err := d.QM.Enroll(quartermaster.EnrollRequest{
		Name:        req.Name,
		IP:          req.IP,
		SSHUser:     req.SSHUser,
		SSHPassword: req.SSHPassword,
		Tailscale:   req.Tailscale,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "enroll_failed", err.Error())
		return
	}
	host, _ := d.Store.Get(hostID)
	writeJSON(w, http.StatusAccepted, map[string]any{"host": host, "jobId": jobID})
}

// editHostRequest carries the editable host fields; nil fields are left unchanged.
type editHostRequest struct {
	Name      *string `json:"name"`
	IP        *string `json:"ip"`
	SSHUser   *string `json:"sshUser"`
	Tailscale *bool   `json:"tailscale"`
}

func (d Deps) editHost(w http.ResponseWriter, r *http.Request) {
	var req editHostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	ok := d.Store.Edit(r.PathValue("id"), func(h *state.Host) {
		if req.Name != nil {
			h.Name = *req.Name
		}
		if req.IP != nil {
			h.IP = *req.IP
		}
		if req.SSHUser != nil {
			h.SSHUser = *req.SSHUser
		}
		if req.Tailscale != nil {
			h.Tailscale = *req.Tailscale
		}
	})
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "host not found")
		return
	}
	host, _ := d.Store.Get(r.PathValue("id"))
	writeJSON(w, http.StatusOK, host)
}

// rotateCert issues and pushes a fresh client certificate to a connected host.
func (d Deps) rotateCert(w http.ResponseWriter, r *http.Request) {
	if err := d.RotateCert(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusConflict, "rotate_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteHost removes a host and revokes its client certificate.
func (d Deps) deleteHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	host, _ := d.Store.Get(id)
	if err := d.Store.Remove(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	if host.CertSerial != "" {
		d.CA.Revoke(host.CertSerial)
		d.Aud.Record("cert_revoked", id, "user", "client certificate revoked", nil)
	}
	d.Aud.Record("host_removed", id, "user", "host removed", nil)
	w.WriteHeader(http.StatusNoContent)
}
