package build

import (
	"debug/buildinfo"
	"runtime/debug"
	"strings"
)

// stampPath is the linker target codeID is stamped through. Spelled out in full because
// CodeIDOfFile has to recognise it inside another binary's recorded -ldflags, where it appears
// as text rather than as a symbol.
const stampPath = "github.com/thinkaliker/labassistant/internal/build.codeID"

// codeID is the fingerprint of the source an upgrade ships — the associate and its privileged
// helper (see internal/codefp) — stamped at link time by scripts/manage.sh:
//
//	go build -ldflags "-X <stampPath>=$(go run ./cmd/codeid)" ./cmd/associate
//
// It is deliberately not a commit: the commit moves on every change to the repo, so comparing
// commits reports every host as stale after a dashboard edit. This moves only when code the
// associate compiles changes.
//
// Empty in an unstamped build (plain `go build`, `go run`, tests). Empty means "unknown" —
// callers must fall back to the revision rather than treat it as a value to compare.
var codeID string

// CodeID returns this binary's stamped associate-code fingerprint, or "" when unstamped.
func CodeID() string { return codeID }

// CodeIDOfFile returns the fingerprint stamped into another binary on disk — for the manager,
// the associate build it would upload to a host. Empty when the file is missing, is not a Go
// binary, or was built without the stamp.
//
// It reads the value back out of the recorded -ldflags rather than from a dedicated section
// because that is where the toolchain preserves it: `go build` records the exact -ldflags
// string in the build info of every binary it links.
func CodeIDOfFile(path string) string {
	if path == "" {
		return ""
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}
	return codeIDFromSettings(info.Settings)
}

func codeIDFromSettings(settings []debug.BuildSetting) string {
	for _, s := range settings {
		if s.Key == "-ldflags" {
			return codeIDFromLDFlags(s.Value)
		}
	}
	return ""
}

// codeIDFromLDFlags extracts the value of `-X <stampPath>=<value>`. Both spellings the linker
// accepts are handled: "-X path=value" as two arguments and "-X=path=value" as one. Splitting
// on whitespace is safe only because a fingerprint is hex — a value that could contain spaces
// would need real shell-quoting rules.
func codeIDFromLDFlags(flags string) string {
	fields := strings.Fields(flags)
	for i, f := range fields {
		var kv string
		switch {
		case f == "-X" && i+1 < len(fields):
			kv = fields[i+1]
		case strings.HasPrefix(f, "-X="):
			kv = strings.TrimPrefix(f, "-X=")
		default:
			continue
		}
		// Quotes can wrap the whole assignment (`-X "path=value"`) or just the value, since
		// the toolchain records the -ldflags string exactly as it was written.
		kv = strings.Trim(kv, `"'`)
		if v, ok := strings.CutPrefix(kv, stampPath+"="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}
