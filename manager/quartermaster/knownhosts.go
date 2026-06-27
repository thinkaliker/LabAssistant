package quartermaster

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"

	"golang.org/x/crypto/ssh"
)

var knownHostsMu sync.Mutex

// hostKeyCallback returns an SSH host-key verifier implementing trust-on-first-use: an
// unseen host's key is recorded; a seen host whose key changed is rejected (possible MITM).
// The SSH host key is the trust anchor for the mTLS bootstrap, so a mismatch must fail.
func (s SSHInstaller) hostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		return verifyKnownHost(s.KnownHostsPath, hostname, key)
	}
}

func verifyKnownHost(path, host string, key ssh.PublicKey) error {
	if path == "" {
		return fmt.Errorf("no known_hosts path configured")
	}
	fp := base64.StdEncoding.EncodeToString(key.Marshal())

	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()

	m := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if existing, ok := m[host]; ok {
		if existing != fp {
			return fmt.Errorf("host key mismatch for %s (possible MITM); remove it from known_hosts to re-trust", host)
		}
		return nil
	}
	m[host] = fp
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
