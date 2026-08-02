package build

// AssociateStale reports whether a host's associate should be replaced with the build the
// manager holds. It is the single definition of "out of date" — the API, the bulk upgrade
// selection, and the dashboard banner all have to agree, or the banner counts hosts the bulk
// upgrade then refuses to touch.
//
// Fingerprints win when both sides have one: they move only when code the associate compiles
// changes, so a manager update that touched only the dashboard leaves every host alone.
//
// Revisions are the fallback for the mixed fleet you get right after adopting the stamp: a
// host still running a pre-stamp associate reports no fingerprint, and there is no way to tell
// what code it holds, so it is treated as stale until one upgrade brings it onto a stamped
// build. Once both sides are stamped the revision is never consulted again.
//
// Unknown on either side of both pairs means "no evidence", which is not the same as stale:
// an offline host reports nothing, and a manager whose associate binary carries neither stamp
// nor VCS info has nothing to compare. Neither is a reason to push a binary at a host.
func AssociateStale(targetCodeID, targetRev, hostCodeID, hostRev string) bool {
	if targetCodeID != "" && hostCodeID != "" {
		return targetCodeID != hostCodeID
	}
	if targetRev != "" && hostRev != "" {
		return targetRev != hostRev
	}
	return false
}
