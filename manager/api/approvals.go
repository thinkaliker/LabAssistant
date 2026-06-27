package api

import "net/http"

func (d Deps) listApprovals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Runner.Approvals())
}

func (d Deps) confirmApproval(w http.ResponseWriter, r *http.Request) {
	out, err := d.Runner.Confirm(r.PathValue("id"))
	if err != nil {
		writeRunnerErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

func (d Deps) rejectApproval(w http.ResponseWriter, r *http.Request) {
	if !d.Runner.Reject(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "not_found", "approval not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
