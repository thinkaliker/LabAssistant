package quartermaster

import (
	"errors"
	"fmt"
	"testing"
)

// The classification decides whether a failed bulk upgrade stops to ask the operator for a
// password. Both directions of a mistake are visible to them: a missed auth failure looks like
// a broken host, and a misclassified one asks for a password that cannot help.
func TestClassifyAuth(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			"ssh handshake rejection",
			fmt.Errorf("ssh dial 10.0.0.5:22: %w", errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain")),
			true,
		},
		{"nothing to offer", errors.New("no SSH auth method (no usable key or agent, and no password given)"), true},
		{"server said no", errors.New("Permission denied (publickey,password)"), true},
		{"sudo rejected the password", errors.New("Process exited with status 1: sudo authentication failed (check the SSH/sudo password, or grant passwordless sudo)"), true},
		{"encrypted key, no passphrase", errors.New("ssh: this private key is passphrase protected: incorrect passphrase supplied"), true},

		// The bulk upgrade's first pass sends no password, so sshRun adds no `sudo -S -v`
		// preamble and the script's plain `sudo` calls fail on any host without a passwordless
		// rule. These are what such a host actually returns — a healthy host that needs asking,
		// not a broken one. Missing them made those hosts fail outright with "status 1".
		{"sudo has no tty", errors.New("upgrade: Process exited with status 1: sudo: no tty present and no askpass program specified"), true},
		{"sudo wants a terminal", errors.New("upgrade: Process exited with status 1: sudo: a terminal is required to read the password; either use the -S option to read from standard input or configure an askpass helper"), true},
		{"sudo password required", errors.New("upgrade: Process exited with status 1: sudo: a password is required"), true},
		{"sudo retry exhausted", errors.New("upgrade: Process exited with status 1: Sorry, try again."), true},

		// The manager offers every agent key plus the ~/.ssh defaults, so a host that accepts
		// none of them trips sshd's MaxAuthTries before running out of methods locally. The
		// server disconnects and the rejection never arrives in the form above.
		{"max auth tries tripped", errors.New("ssh dial 10.0.0.5:22: ssh: handshake failed: ssh: disconnect, reason 2: Too many authentication failures"), true},
		{"client ran out of methods", errors.New("ssh: handshake failed: ssh: no more authentication methods available"), true},
		{"server hung up mid-auth", errors.New("ssh dial 10.0.0.5:22: ssh: handshake failed: EOF"), true},

		// No password fixes any of these, so the operator must not be prompted for one.
		{"host is down", errors.New("ssh dial 10.0.0.5:22: dial tcp 10.0.0.5:22: connect: connection refused"), false},
		{"host key changed", errors.New("ssh: host key mismatch for 10.0.0.5"), false},
		{"upload failed", errors.New("sftp: write /opt/labassistant/associate: no space left on device"), false},
		{"no upgrade path", errors.New("no upgrade path available for this host"), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyAuth(tt.err)
			if errors.Is(got, ErrAuth) != tt.want {
				t.Errorf("classifyAuth(%v): ErrAuth = %v, want %v", tt.err, !tt.want, tt.want)
			}
			if tt.err == nil {
				return
			}
			// The original text has to survive: it is what the operator reads to work out
			// which account the host actually wants.
			if !errors.Is(got, tt.err) {
				t.Errorf("classifyAuth dropped the original error: %v", got)
			}
		})
	}
}

// classifyAuth runs on errors that have already been through it in some call paths (dial's
// result is wrapped again by its callers), and must not stack wrappers.
func TestClassifyAuthIsIdempotent(t *testing.T) {
	once := classifyAuth(errors.New("ssh: unable to authenticate"))
	twice := classifyAuth(once)
	if once.Error() != twice.Error() {
		t.Errorf("re-classifying changed the error: %q -> %q", once.Error(), twice.Error())
	}
}
