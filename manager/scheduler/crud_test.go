package scheduler

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPruneHosts(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "tasks.json"), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	multi, err := s.Create(Task{Name: "multi", Schedule: "0 3 * * *", HostIDs: []string{"a", "gone"}, Module: "m", Action: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	only, err := s.Create(Task{Name: "only", Schedule: "0 3 * * *", HostIDs: []string{"gone"}, Module: "m", Action: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	untouched, err := s.Create(Task{Name: "untouched", Schedule: "0 3 * * *", HostIDs: []string{"a"}, Module: "m", Action: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	changed := s.PruneHosts(func(id string) bool { return id != "gone" })
	if len(changed) != 2 {
		t.Fatalf("changed %d tasks, want 2", len(changed))
	}

	got, _ := s.Get(multi.ID)
	if len(got.HostIDs) != 1 || got.HostIDs[0] != "a" || !got.Enabled {
		t.Errorf("multi = %v enabled=%v, want [a] enabled", got.HostIDs, got.Enabled)
	}
	got, _ = s.Get(only.ID)
	if len(got.HostIDs) != 0 || got.Enabled {
		t.Errorf("only = %v enabled=%v, want [] disabled", got.HostIDs, got.Enabled)
	}
	got, _ = s.Get(untouched.ID)
	if len(got.HostIDs) != 1 || !got.Enabled {
		t.Errorf("untouched = %v enabled=%v, want unchanged", got.HostIDs, got.Enabled)
	}

	// persisted: a reload sees the pruned state
	r, err := Load(s.path, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(only.ID); got.Enabled {
		t.Error("reloaded task still enabled")
	}
}

func TestFireSkipsUnknownHosts(t *testing.T) {
	var hit []string
	dispatch := func(hostID, _, _ string, _ json.RawMessage) error {
		hit = append(hit, hostID)
		return nil
	}
	s, err := Load(filepath.Join(t.TempDir(), "tasks.json"), dispatch, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetKnown(func(id string) bool { return id != "gone" })

	both, _ := s.Create(Task{Name: "both", Schedule: "0 3 * * *", HostIDs: []string{"gone", "a"}, Module: "m", Action: "x", Enabled: true})
	s.fire(both)
	if len(hit) != 1 || hit[0] != "a" {
		t.Fatalf("dispatched to %v, want [a]", hit)
	}
	if got, _ := s.Get(both.ID); got.LastStatus != "dispatched" {
		t.Errorf("status %q, want dispatched", got.LastStatus)
	}

	hit = nil
	orphan, _ := s.Create(Task{Name: "orphan", Schedule: "0 3 * * *", HostIDs: []string{"gone"}, Module: "m", Action: "x", Enabled: true})
	s.fire(orphan)
	if len(hit) != 0 {
		t.Fatalf("dispatched to %v, want none", hit)
	}
	if got, _ := s.Get(orphan.ID); got.LastStatus != "skipped" {
		t.Errorf("status %q, want skipped", got.LastStatus)
	}
}
