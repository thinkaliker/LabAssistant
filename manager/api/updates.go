package api

import (
	"encoding/json"
	"net/http"
)

// isDigest reports whether s is a well-formed "sha256:<64 hex>" digest. Used to reject phantom
// container updates: a stale associate status (reported before the associate's remote-digest
// parsing was hardened) can carry a bogus latest like "Name: ghcr.io/..." that would otherwise
// render as an uninstallable update. Digests are the only thing the update flow can act on.
func isDigest(s string) bool {
	const p = "sha256:"
	if len(s) != len(p)+64 || s[:len(p)] != p {
		return false
	}
	for _, c := range s[len(p):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// pkgUpdate is one upgradable host package and its version transition.
type pkgUpdate struct {
	Name      string `json:"name"`
	Current   string `json:"current,omitempty"`
	Candidate string `json:"candidate,omitempty"`
}

// osUpdate is one host's pending OS package updates (from the qup module).
type osUpdate struct {
	HostID   string      `json:"hostId"`
	HostName string      `json:"hostName"`
	Count    int         `json:"count"`
	Mode     string      `json:"mode"`
	Packages []pkgUpdate `json:"packages"`
}

// containerUpdate is one compose service with a newer image available (from the duo module).
type containerUpdate struct {
	HostID   string `json:"hostId"`
	HostName string `json:"hostName"`
	Stack    string `json:"stack"`
	Service  string `json:"service"`
	Image    string `json:"image"`
	Current  string `json:"current,omitempty"`
	Latest   string `json:"latest,omitempty"`
}

// qupStatus is the shape qup reports in its module status.
type qupStatus struct {
	Count    int         `json:"count"`
	Packages []pkgUpdate `json:"packages"`
	Mode     string      `json:"mode"`
}

// updates is a read-only projection over the qup and duo modules across all hosts, listing
// what has updates available. Applying them goes through the normal action/approval flow.
func (d Deps) updates(w http.ResponseWriter, r *http.Request) {
	osUpdates := []osUpdate{}
	containers := []containerUpdate{}
	for _, h := range d.Store.Hosts() {
		for _, m := range h.Modules {
			if len(m.Status) == 0 {
				continue
			}
			switch m.Name {
			case "qup":
				var qs qupStatus
				if json.Unmarshal(m.Status, &qs) != nil {
					continue
				}
				osUpdates = append(osUpdates, osUpdate{
					HostID: h.ID, HostName: h.Name, Count: qs.Count,
					Mode: qs.Mode, Packages: qs.Packages,
				})
			case "duo":
				var ds duoStatus
				if json.Unmarshal(m.Status, &ds) != nil {
					continue
				}
				for _, s := range ds.Stacks {
					for _, sv := range s.Services {
						if !sv.UpdateAvailable {
							continue
						}
						// Skip phantom updates from stale reports: an update the flow can't act
						// on (its digests aren't real) is worse than showing nothing.
						if !isDigest(sv.CurrentDigest) || !isDigest(sv.LatestDigest) {
							continue
						}
						containers = append(containers, containerUpdate{
							HostID: h.ID, HostName: h.Name, Stack: s.Name,
							Service: sv.Name, Image: sv.Image,
							Current: sv.CurrentDigest, Latest: sv.LatestDigest,
						})
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"os": osUpdates, "containers": containers})
}
