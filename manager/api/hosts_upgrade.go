package api

import (
	"net/http"
	"sort"

	"github.com/thinkaliker/labassistant/internal/build"
	"github.com/thinkaliker/labassistant/manager/quartermaster"
	"github.com/thinkaliker/labassistant/manager/state"
)

// staleHosts returns the hosts whose associate is not the build the manager holds, ordered by
// name so the count and the job list read the same way twice running.
//
// This is the one place the fleet is selected. The bulk upgrade and the overview count both
// call it, so the banner can never offer to upgrade a set the endpoint would then decline to
// touch.
func (d Deps) staleHosts() []state.Host {
	var out []state.Host
	for _, h := range d.Store.Hosts() {
		if build.AssociateStale(d.AssociateCodeID, d.AssociateBuild, h.AssociateCodeID, h.AssociateVersion) {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// upgradeStale pushes the manager's associate build to every host that is running different
// associate code, and returns one progress job per host.
//
// Hosts whose associate code already matches are not touched, even when the manager itself is
// several commits newer: an upgrade restarts the associate and interrupts whatever it is
// running, which is not a cost worth paying to redeploy identical code.
//
// It takes no credentials, deliberately. Hosts in a fleet do not necessarily share a login, so
// one password for the batch would be wrong for most of them; each host is tried with the
// manager's key/agent and its own stored SSH user, and the ones that come back needing a login
// are retried individually through POST /hosts/{id}/upgrade. A job that failed that way is
// marked authRequired in its result so the caller can tell it apart from a real breakage.
//
// Failures are reported per host rather than aborting the batch. A single host with no SSH
// path (a local-mode associate, say) must not strand the rest of the fleet on old code.
func (d Deps) upgradeStale(w http.ResponseWriter, r *http.Request) {
	started := []map[string]string{}
	failed := []map[string]string{}
	for _, h := range d.staleHosts() {
		jobID, err := d.QM.Upgrade(quartermaster.UpgradeRequest{
			HostID:  h.ID,
			SSHUser: h.SSHUser,
		})
		if err != nil {
			failed = append(failed, map[string]string{"hostId": h.ID, "hostName": h.Name, "error": err.Error()})
			continue
		}
		started = append(started, map[string]string{"hostId": h.ID, "hostName": h.Name, "jobId": jobID})
	}

	// 202 even when nothing matched: "no host needs this" is the expected, healthy answer most
	// of the time, not an error the dashboard should surface as a failed request.
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started, "failed": failed})
}
