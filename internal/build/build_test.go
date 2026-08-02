package build

import (
	"runtime/debug"
	"testing"
)

func TestRevisionOf(t *testing.T) {
	const full = "de068dffd2e95ff92eebe2b471c7f5ecdae18edb"
	cases := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{"clean build", []debug.BuildSetting{
			{Key: "vcs.revision", Value: full}, {Key: "vcs.modified", Value: "false"},
		}, "de068dffd2e9"},
		{"dirty tree", []debug.BuildSetting{
			{Key: "vcs.revision", Value: full}, {Key: "vcs.modified", Value: "true"},
		}, "de068dffd2e9+dirty"},
		// No stamp (go run, tests, -buildvcs=false): unknown, never a comparable version.
		{"no vcs stamp", []debug.BuildSetting{{Key: "GOARCH", Value: "arm64"}}, ""},
		{"no settings", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := revisionOf(c.settings); got != c.want {
				t.Errorf("revisionOf() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRevisionOfFileMissing(t *testing.T) {
	if got := RevisionOfFile(t.TempDir() + "/nope"); got != "" {
		t.Errorf("RevisionOfFile(missing) = %q, want empty", got)
	}
	if got := RevisionOfFile(""); got != "" {
		t.Errorf("RevisionOfFile(\"\") = %q, want empty", got)
	}
}
