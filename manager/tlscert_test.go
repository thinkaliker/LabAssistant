package manager

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writePair writes a self-signed cert/key for cn and stamps both files with mod.
func writePair(t *testing.T, certFile, keyFile, cn string, mod time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

func servedCN(t *testing.T, r *certReloader) string {
	t.Helper()
	c, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.Subject.CommonName
}

func TestCertReloaderPicksUpRenewal(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	writePair(t, certFile, keyFile, "old", t0)

	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if cn := servedCN(t, r); cn != "old" {
		t.Fatalf("served %q, want old", cn)
	}

	writePair(t, certFile, keyFile, "new", t0.Add(time.Minute))
	// Within the check interval the cached pair is still served.
	if cn := servedCN(t, r); cn != "old" {
		t.Fatalf("re-read before the check interval: served %q", cn)
	}
	r.checked = time.Time{}
	if cn := servedCN(t, r); cn != "new" {
		t.Fatalf("after renewal served %q, want new", cn)
	}
}

// TestCertReloaderKeepsOldPairOnBadRenewal: a renewal caught half-written (cert replaced, key
// not yet) must not take the dashboard down, and is picked up once the key lands.
func TestCertReloaderKeepsOldPairOnBadRenewal(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	writePair(t, certFile, keyFile, "old", t0)
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}

	// Stage a fresh pair elsewhere, then copy only its cert in: cert and key now mismatch.
	stage := t.TempDir()
	newCert, newKey := filepath.Join(stage, "cert.pem"), filepath.Join(stage, "key.pem")
	writePair(t, newCert, newKey, "new", t0.Add(time.Minute))
	copyFile(t, newCert, certFile, t0.Add(time.Minute))

	r.checked = time.Time{}
	if cn := servedCN(t, r); cn != "old" {
		t.Fatalf("mismatched pair served %q, want old kept", cn)
	}

	copyFile(t, newKey, keyFile, t0.Add(2*time.Minute))
	r.checked = time.Time{}
	if cn := servedCN(t, r); cn != "new" {
		t.Fatalf("completed renewal served %q, want new", cn)
	}
}

func TestNewCertReloaderFailsOnMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := newCertReloader(filepath.Join(dir, "nope.pem"), filepath.Join(dir, "nope.key")); err == nil {
		t.Fatal("want an error for missing files")
	}
}

func copyFile(t *testing.T, src, dst string, mod time.Time) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dst, mod, mod); err != nil {
		t.Fatal(err)
	}
}
