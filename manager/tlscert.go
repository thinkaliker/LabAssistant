package manager

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// certCheckInterval bounds how often the dashboard's certificate files are re-stat'ed.
const certCheckInterval = 30 * time.Second

// certReloader serves the dashboard's TLS certificate from disk and picks up a replacement
// (e.g. `tailscale cert` or certbot renewing it) without a manager restart. A replacement that
// fails to load — such as the cert written before its key — keeps the previous pair in service
// and is retried on the next check.
type certReloader struct {
	certFile, keyFile string

	mu              sync.Mutex
	cert            *tls.Certificate
	certMod, keyMod time.Time
	checked         time.Time
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	certMod, keyMod, err := r.modTimes()
	if err != nil {
		return nil, err
	}
	if err := r.load(certMod, keyMod); err != nil {
		return nil, err
	}
	r.checked = time.Now()
	return r, nil
}

func (r *certReloader) modTimes() (certMod, keyMod time.Time, err error) {
	ci, err := os.Stat(r.certFile)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	ki, err := os.Stat(r.keyFile)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return ci.ModTime(), ki.ModTime(), nil
}

// load reads the pair and, only on success, records it with the mod times it was read at.
func (r *certReloader) load(certMod, keyMod time.Time) error {
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate %s / key %s: %w", r.certFile, r.keyFile, err)
	}
	r.cert, r.certMod, r.keyMod = &c, certMod, keyMod
	return nil
}

// GetCertificate implements tls.Config.GetCertificate.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.checked) >= certCheckInterval {
		r.checked = time.Now()
		certMod, keyMod, err := r.modTimes()
		switch {
		case err != nil:
			slog.Warn("TLS certificate check failed; keeping the loaded certificate", "err", err)
		case !certMod.Equal(r.certMod) || !keyMod.Equal(r.keyMod):
			if err := r.load(certMod, keyMod); err != nil {
				slog.Warn("TLS certificate changed but did not load; keeping the previous one", "err", err)
			} else {
				slog.Info("reloaded TLS certificate", "cert", r.certFile)
			}
		}
	}
	return r.cert, nil
}
