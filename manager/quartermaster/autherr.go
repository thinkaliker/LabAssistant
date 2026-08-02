package quartermaster

import (
	"errors"
	"fmt"
	"strings"
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

// authPhrases are the messages that mean "wrong or missing credentials". Matching on text is
// unpleasant, but x/crypto/ssh reports authentication rejection as an opaque error built from
// the server's response rather than as a typed value, and the sudo case originates in a remote
// shell where nothing but the message survives.
//
// Anything not listed stays unclassified. A false negative costs an operator one manual retry;
// a false positive would prompt for a password that cannot fix the problem, so the list is
// deliberately narrow.
var authPhrases = []string{
	"unable to authenticate",        // ssh: handshake failed: ssh: unable to authenticate...
	"no supported methods remain",   // trailing half of the same message
	"permission denied",             // server-side rejection surfaced verbatim
	"sudo authentication failed",    // sshRun's own probe (see the sudo -S -v preamble)
	"no ssh auth method",            // dial gave up before connecting: nothing to offer
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
	for _, p := range authPhrases {
		if strings.Contains(msg, p) {
			return fmt.Errorf("%w: %w", ErrAuth, err)
		}
	}
	return err
}
