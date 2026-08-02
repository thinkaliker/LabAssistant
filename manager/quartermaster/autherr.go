package quartermaster

import (
	"errors"
	"fmt"
	"strings"

	"github.com/thinkaliker/labassistant/internal/elevated"
)

// ErrAuth marks a failure the operator can fix by supplying credentials for that specific
// host — a rejected SSH login, an encrypted key with no passphrase, or a sudo password the
// host would not accept.
//
// It exists so a bulk operation can tell "this host needs a password from you" apart from
// "this host is broken". Those need opposite responses: the first is worth stopping to ask
// about, the second is not, and a fleet where every host has different credentials produces
// a stream of the first that would otherwise be indistinguishable from real failures.
var ErrAuth = errors.New("ssh authentication failed")

// authPhrases are the SSH-side messages that mean "wrong or missing credentials". Matching on
// text is unpleasant, but x/crypto/ssh reports authentication rejection as an opaque error
// built from the server's response rather than as a typed value.
//
// The sudo half of the problem is not listed here: elevated.IsSudoAuthFailure already owns
// those phrases for the associate's own helper, and the remote scripts run under sudo too, so
// classifyAuth defers to it rather than keeping a second list to drift out of sync.
//
// Anything unlisted stays unclassified. A false negative costs an operator one manual retry;
// a false positive prompts for a password that cannot fix the problem, so the list is narrow.
var authPhrases = []string{
	"unable to authenticate",        // ssh: handshake failed: ssh: unable to authenticate...
	"no supported methods remain",   // trailing half of the same message
	"permission denied",             // server-side rejection surfaced verbatim
	"no ssh auth method",            // dial gave up before connecting: nothing to offer
	"sudo authentication failed",    // sshRun's own `sudo -S -v` probe, i.e. the password was wrong
	"incorrect passphrase",          // encrypted private key, no/blank passphrase given
	"decryption password incorrect", // same, from another key format
}

// classifyAuth wraps err with ErrAuth when it reads as a credentials problem, and returns it
// unchanged otherwise. Wrapping preserves the original text: the operator still needs to see
// what the host actually said.
func classifyAuth(err error) error {
	if err == nil || errors.Is(err, ErrAuth) {
		return err
	}
	msg := strings.ToLower(err.Error())
	// The remote scripts are all sudo, and the first pass of a bulk upgrade deliberately
	// carries no password, so "sudo wants a password" is the single most common way a healthy
	// host fails here. Missing it is what made those hosts look broken instead of asking.
	if elevated.IsSudoAuthFailure(msg) {
		return fmt.Errorf("%w: %w", ErrAuth, err)
	}
	for _, p := range authPhrases {
		if strings.Contains(msg, p) {
			return fmt.Errorf("%w: %w", ErrAuth, err)
		}
	}
	return err
}
