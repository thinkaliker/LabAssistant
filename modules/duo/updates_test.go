package duo

import (
	"encoding/json"
	"testing"
)

const (
	digestA = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestB = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func TestHasUpdate(t *testing.T) {
	cases := []struct {
		name string
		u    imageUpdate
		want bool
	}{
		{"real change", imageUpdate{Current: digestA, Latest: digestB}, true},
		{"same digest", imageUpdate{Current: digestA, Latest: digestA}, false},
		{"empty latest", imageUpdate{Current: digestA}, false},
		// An older associate stored whole `imagetools inspect` output as the latest digest;
		// that must never count as an update — nothing can install it.
		{"garbage latest", imageUpdate{Current: digestA, Latest: "Name:      ghcr.io/foo/bar:latest"}, false},
		{"garbage current", imageUpdate{Current: "Name:      ghcr.io/foo/bar:latest", Latest: digestB}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.u.hasUpdate(); got != c.want {
				t.Errorf("hasUpdate(%+v) = %v, want %v", c.u, got, c.want)
			}
		})
	}
}

// A check that cannot compare an image must clear whatever verdict was held for it. Merging
// only (the old behaviour) let a bogus entry survive every later check.
func TestIngestResultClearsUnknown(t *testing.T) {
	m := &Module{updates: map[string]imageUpdate{
		"stale:latest": {Current: digestA, Latest: "Name:      ghcr.io/foo/bar:latest"},
		"keep:latest":  {Current: digestA, Latest: digestB},
	}}
	data, err := json.Marshal(map[string]any{
		"images":  map[string]imageUpdate{"fresh:latest": {Current: digestA, Latest: digestB}},
		"unknown": []string{"stale:latest"},
	})
	if err != nil {
		t.Fatal(err)
	}

	m.IngestResult("check-updates", data)

	if _, ok := m.updates["stale:latest"]; ok {
		t.Error("stale:latest still present after being reported unknown")
	}
	if got := m.updates["fresh:latest"]; got.Latest != digestB {
		t.Errorf("fresh:latest = %+v, want latest %s", got, digestB)
	}
	if _, ok := m.updates["keep:latest"]; !ok {
		t.Error("keep:latest was dropped; only checked images should change")
	}
}
