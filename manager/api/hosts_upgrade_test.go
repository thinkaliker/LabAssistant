package api

import (
	"path/filepath"
	"testing"

	"github.com/thinkaliker/labassistant/manager/state"
)

// newUpgradeDeps builds a Deps whose store holds hosts already reporting the given associate
// code, as if each had connected. SetOnline is the only path that populates the runtime build
// fields, so the test goes through it rather than writing the struct directly.
func newUpgradeDeps(t *testing.T, targetCodeID, targetRev string, hosts map[string][2]string) Deps {
	t.Helper()
	store, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	for name, rep := range hosts {
		if err := store.Add(state.Host{ID: name, Name: name, IP: "10.0.0.1"}); err != nil {
			t.Fatalf("add host %s: %v", name, err)
		}
		store.SetOnline(name, rep[1], rep[0], nil)
	}
	return Deps{Store: store, AssociateCodeID: targetCodeID, AssociateBuild: targetRev}
}

func staleNames(d Deps) []string {
	var names []string
	for _, h := range d.staleHosts() {
		names = append(names, h.Name)
	}
	return names
}

// The whole point of the fingerprint: a manager several commits ahead whose associate code is
// unchanged must select nothing. Selecting the fleet here means every dashboard commit
// restarts every associate.
func TestStaleHostsIgnoresCommitOnlyDrift(t *testing.T) {
	d := newUpgradeDeps(t, "code2", "rev9", map[string][2]string{
		"alpha": {"code2", "rev1"},
		"beta":  {"code2", "rev4"},
	})
	if got := staleNames(d); len(got) != 0 {
		t.Errorf("staleHosts = %v, want none (associate code matches on every host)", got)
	}
}

func TestStaleHostsSelectsChangedCodeAndUnknownHosts(t *testing.T) {
	d := newUpgradeDeps(t, "code2", "rev9", map[string][2]string{
		"current": {"code2", "rev9"},
		"changed": {"code1", "rev8"},
		// Pre-stamp associate: reports a commit but no fingerprint, so its code is unknown
		// and it stays selected until one upgrade puts a stamped build on it.
		"unstamped": {"", "rev8"},
		// Offline/never connected: reports nothing at all, which is not evidence of anything.
		"silent": {"", ""},
	})
	got := staleNames(d)
	want := []string{"changed", "unstamped"} // sorted by name
	if len(got) != len(want) {
		t.Fatalf("staleHosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("staleHosts = %v, want %v", got, want)
		}
	}
}

// With no fingerprint on the manager's own associate binary there is nothing to compare
// against, and pushing a binary at every host on that basis would be a guess.
func TestStaleHostsWithNothingKnownSelectsNothing(t *testing.T) {
	d := newUpgradeDeps(t, "", "", map[string][2]string{
		"alpha": {"code1", "rev1"},
	})
	if got := staleNames(d); len(got) != 0 {
		t.Errorf("staleHosts = %v, want none (manager build is unknown)", got)
	}
}
