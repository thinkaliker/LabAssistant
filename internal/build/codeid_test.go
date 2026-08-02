package build

import (
	"runtime/debug"
	"testing"
)

func TestCodeIDFromLDFlags(t *testing.T) {
	tests := []struct {
		name  string
		flags string
		want  string
	}{
		// The two spellings the linker accepts. manage.sh emits the first; a hand-run build or
		// a wrapper that joins flags may emit the second.
		{"separate argument", "-X " + stampPath + "=abc123def456", "abc123def456"},
		{"joined argument", "-X=" + stampPath + "=abc123def456", "abc123def456"},
		{"among other flags", "-s -w -X " + stampPath + "=deadbeef0001 -buildid=x", "deadbeef0001"},
		{"quoted value", `-X "` + stampPath + `=abc123def456"`, "abc123def456"},
		// A build that stamped something else entirely must read as unstamped, not as a
		// fingerprint — comparing another package's version string against a fingerprint
		// would mark every host stale forever.
		{"different symbol", "-X main.version=1.2.3", ""},
		{"no -X at all", "-s -w", ""},
		{"empty", "", ""},
		{"dangling -X", "-s -w -X", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codeIDFromLDFlags(tt.flags); got != tt.want {
				t.Errorf("codeIDFromLDFlags(%q) = %q, want %q", tt.flags, got, tt.want)
			}
		})
	}
}

func TestCodeIDFromSettings(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "-buildmode", Value: "exe"},
		{Key: "-ldflags", Value: "-X " + stampPath + "=0123456789ab"},
		{Key: "vcs.revision", Value: "aaaaaaaaaaaa"},
	}
	if got := codeIDFromSettings(settings); got != "0123456789ab" {
		t.Errorf("codeIDFromSettings = %q, want %q", got, "0123456789ab")
	}
	// A binary built without -ldflags records no such setting at all.
	if got := codeIDFromSettings(settings[:1]); got != "" {
		t.Errorf("codeIDFromSettings(no ldflags) = %q, want empty", got)
	}
}

func TestCodeIDOfFileMissing(t *testing.T) {
	if got := CodeIDOfFile(""); got != "" {
		t.Errorf("CodeIDOfFile(\"\") = %q, want empty", got)
	}
	if got := CodeIDOfFile(t.TempDir() + "/not-a-binary"); got != "" {
		t.Errorf("CodeIDOfFile(missing) = %q, want empty", got)
	}
}
