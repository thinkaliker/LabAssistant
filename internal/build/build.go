// Package build reports which commit a binary was built from. The manager uses it to tell
// whether a host's associate is running the code the manager would deploy today: an associate
// is only replaced at enroll or upgrade time, so without a build identity a host can quietly
// run months-old module code while the manager is current.
package build

import (
	"debug/buildinfo"
	"runtime/debug"
)

// revLen is how much of the commit hash identifies a build in logs and the dashboard.
const revLen = 12

// Revision returns the short commit this binary was built from, "<rev>+dirty" when the tree
// had uncommitted changes, or "" when the binary carries no VCS stamp (`go run`, tests, or a
// build with -buildvcs=false). An empty result means "unknown" — never compare it as if it
// were a version.
func Revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return revisionOf(info.Settings)
}

// RevisionOfFile returns the revision stamped into another binary on disk — for the manager,
// the associate build it would upload to a host. Empty when the file is missing, is not a Go
// binary, or carries no VCS stamp.
func RevisionOfFile(path string) string {
	if path == "" {
		return ""
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	return revisionOf(info.Settings)
}

func revisionOf(settings []debug.BuildSetting) string {
	var rev string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > revLen {
		rev = rev[:revLen]
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}
