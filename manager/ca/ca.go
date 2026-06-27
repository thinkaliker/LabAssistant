// Package ca manages the manager's certificate authority: a self-signed root CA, the
// manager's own server certificate, and client certificates issued to associates. All
// material lives under <data>/certs with strict permissions and is never written to
// state.json.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	caValidity     = 10 * 365 * 24 * time.Hour
	leafValidity   = 365 * 24 * time.Hour
	caCertFile     = "ca.crt"
	caKeyFile      = "ca.key"
	serverCertFile = "server.crt"
	serverKeyFile  = "server.key"
)

// CA is the manager's certificate authority.
type CA struct {
	dir     string
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	caPEM   []byte // PEM-encoded CA certificate (for pinning by clients)
	srvCert tls.Certificate

	mu      sync.Mutex
	revoked map[string]bool // revoked client-cert serials (hex)
}

const revokedFile = "revoked.json"

// LoadOrCreate loads the CA and manager server certificate from dir, generating them on
// first run. serverSANs are the DNS names / IPs the manager is reachable at (the associate
// pins these); "localhost" and "127.0.0.1" are always included for local development.
func LoadOrCreate(dir string, serverSANs []string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create certs dir: %w", err)
	}
	c := &CA{dir: dir, revoked: map[string]bool{}}
	if err := c.loadOrCreateCA(); err != nil {
		return nil, err
	}
	if err := c.loadOrCreateServer(serverSANs); err != nil {
		return nil, err
	}
	c.loadRevoked()
	return c, nil
}

// CAPEM returns the PEM-encoded CA certificate.
func (c *CA) CAPEM() []byte { return c.caPEM }

// LeafValidity is how long issued client/server certs are valid.
func (c *CA) LeafValidity() time.Duration { return leafValidity }

// ServerTLSConfig returns a TLS config for the manager's gRPC server that requires and
// verifies a client certificate signed by this CA (mTLS), and rejects revoked certs.
func (c *CA) ServerTLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(c.caCert)
	return &tls.Config{
		Certificates: []tls.Certificate{c.srvCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no client certificate")
			}
			if c.IsRevoked(serialHex(cs.PeerCertificates[0].SerialNumber)) {
				return errors.New("client certificate is revoked")
			}
			return nil
		},
	}
}

// Revoke marks a client-cert serial revoked and persists the set.
func (c *CA) Revoke(serial string) {
	if serial == "" {
		return
	}
	c.mu.Lock()
	c.revoked[serial] = true
	c.saveRevoked()
	c.mu.Unlock()
}

// IsRevoked reports whether a serial is revoked.
func (c *CA) IsRevoked(serial string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revoked[serial]
}

func (c *CA) loadRevoked() {
	b, err := os.ReadFile(filepath.Join(c.dir, revokedFile))
	if err != nil {
		return
	}
	var list []string
	if json.Unmarshal(b, &list) == nil {
		for _, s := range list {
			c.revoked[s] = true
		}
	}
}

// saveRevoked persists the revoked set. Caller holds the lock.
func (c *CA) saveRevoked() {
	list := make([]string, 0, len(c.revoked))
	for s := range c.revoked {
		list = append(list, s)
	}
	b, _ := json.Marshal(list)
	_ = os.WriteFile(filepath.Join(c.dir, revokedFile), b, 0o600)
}

func serialHex(n *big.Int) string { return n.Text(16) }

// IssueClient mints a client certificate+key for an associate identified by hostID
// (carried as the certificate Common Name). Returns PEM-encoded cert and key plus the
// certificate serial (hex), which the manager stores so the cert can later be revoked.
func (c *CA) IssueClient(hostID string) (certPEM, keyPEM []byte, serial string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}
	sn, err := randSerial()
	if err != nil {
		return nil, nil, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: hostID},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &key.PublicKey, c.caKey)
	if err != nil {
		return nil, nil, "", err
	}
	return encodeCert(der), encodeKey(key), serialHex(sn), nil
}

func (c *CA) loadOrCreateCA() error {
	certPEM, errC := os.ReadFile(filepath.Join(c.dir, caCertFile))
	keyPEM, errK := os.ReadFile(filepath.Join(c.dir, caKeyFile))
	if errC == nil && errK == nil {
		cert, key, err := parseCertKey(certPEM, keyPEM)
		if err != nil {
			return fmt.Errorf("load CA: %w", err)
		}
		c.caCert, c.caKey, c.caPEM = cert, key, certPEM
		return nil
	}
	if !errors.Is(errC, fs.ErrNotExist) && errC != nil {
		return errC
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "LabAssistant Root CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	c.caCert, c.caKey, c.caPEM = cert, key, encodeCert(der)
	if err := writeFile(filepath.Join(c.dir, caCertFile), c.caPEM, 0o644); err != nil {
		return err
	}
	return writeFile(filepath.Join(c.dir, caKeyFile), encodeKey(key), 0o600)
}

func (c *CA) loadOrCreateServer(sans []string) error {
	certPEM, errC := os.ReadFile(filepath.Join(c.dir, serverCertFile))
	keyPEM, errK := os.ReadFile(filepath.Join(c.dir, serverKeyFile))
	if errC == nil && errK == nil {
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return fmt.Errorf("load server cert: %w", err)
		}
		c.srvCert = pair
		return nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randSerial()
	if err != nil {
		return err
	}
	dns, ips := splitSANs(append([]string{"localhost", "127.0.0.1"}, sans...))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "labassistant-manager"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &key.PublicKey, c.caKey)
	if err != nil {
		return err
	}
	srvCertPEM, srvKeyPEM := encodeCert(der), encodeKey(key)
	if err := writeFile(filepath.Join(c.dir, serverCertFile), srvCertPEM, 0o644); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(c.dir, serverKeyFile), srvKeyPEM, 0o600); err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		return err
	}
	c.srvCert = pair
	return nil
}

func parseCertKey(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, errors.New("invalid certificate PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, errors.New("invalid key PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func encodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func encodeKey(key *ecdsa.PrivateKey) []byte {
	der, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func randSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func splitSANs(sans []string) (dns []string, ips []net.IP) {
	seen := map[string]bool{}
	for _, s := range sans {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		if ip := net.ParseIP(s); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, s)
		}
	}
	return dns, ips
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
