package build

import "testing"

func TestAssociateStale(t *testing.T) {
	tests := []struct {
		name                                         string
		targetCodeID, targetRev, hostCodeID, hostRev string
		want                                         bool
	}{
		// The case this whole mechanism exists for: the manager moved several commits ahead,
		// but nothing the associate compiles changed, so the fleet must be left alone.
		{"same code, newer manager commit", "aaa", "rev2", "aaa", "rev1", false},
		{"different code", "bbb", "rev2", "aaa", "rev1", true},

		// Fallback while the fleet is mixed: a host on a pre-stamp associate reports no
		// fingerprint, so there is no way to know what code it holds.
		{"host predates the stamp, revs differ", "aaa", "rev2", "", "rev1", true},
		{"host predates the stamp, revs match", "aaa", "rev1", "", "rev1", false},
		{"neither side stamped, revs differ", "", "rev2", "", "rev1", true},

		// No evidence is not staleness: never push a binary at a host on a guess.
		{"host reports nothing at all", "aaa", "rev2", "", "", false},
		{"manager knows nothing", "", "", "aaa", "rev1", false},
		{"nothing known anywhere", "", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssociateStale(tt.targetCodeID, tt.targetRev, tt.hostCodeID, tt.hostRev)
			if got != tt.want {
				t.Errorf("AssociateStale(%q,%q,%q,%q) = %v, want %v",
					tt.targetCodeID, tt.targetRev, tt.hostCodeID, tt.hostRev, got, tt.want)
			}
		})
	}
}
