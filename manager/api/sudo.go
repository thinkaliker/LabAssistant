package api

import (
	"encoding/json"
	"net/http"
)

func (d Deps) listSudoPrompts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Runner.SudoPrompts())
}

// submitSudo re-dispatches a paused elevated action with the operator-supplied sudo
// password. The password is read from the request body and forwarded to the host over the
// mTLS stream; it is never persisted or written to the audit log.
func (d Deps) submitSudo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if req.Password == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "password required")
		return
	}
	out, err := d.Runner.SubmitSudo(r.PathValue("id"), req.Password)
	if err != nil {
		writeRunnerErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (d Deps) cancelSudo(w http.ResponseWriter, r *http.Request) {
	if !d.Runner.CancelSudo(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "not_found", "sudo prompt not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
