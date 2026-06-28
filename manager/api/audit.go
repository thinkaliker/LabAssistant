package api

import (
	"net/http"
	"strconv"
)

// audit returns recent audit entries (newest first). Optional ?limit=N.
func (d Deps) audit(w http.ResponseWriter, r *http.Request) {
	if !d.auditReadAllowed(r) {
		writeErr(w, http.StatusForbidden, "forbidden", "audit access not permitted for this credential")
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, d.Aud.List(limit))
}
