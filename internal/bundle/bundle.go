// Package bundle is the enrollment bundle an associate uses to dial home: the host
// identity, the manager address, the pinned CA, and the associate's issued client
// certificate. In Slice 1 the manager mints it and it is copied to the host manually;
// later the quartermaster delivers it over SSH.
package bundle

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Bundle is everything an associate needs to establish its mTLS stream.
type Bundle struct {
	HostID      string `json:"hostId"`
	ManagerAddr string `json:"managerAddr"` // host:port the associate dials
	ServerName  string `json:"serverName"`  // expected manager cert SAN
	CACert      []byte `json:"caCert"`      // PEM, pinned
	ClientCert  []byte `json:"clientCert"`  // PEM
	ClientKey   []byte `json:"clientKey"`   // PEM
}

// Save writes the bundle as JSON with owner-only permissions (it contains a private key).
func (b Bundle) Save(path string) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads a bundle from path.
func Load(path string) (Bundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return Bundle{}, fmt.Errorf("parse bundle %s: %w", path, err)
	}
	return b, nil
}

// ClientTLSConfig builds the associate's mTLS client config: its client certificate, the
// pinned CA as the only trusted root, and the expected manager server name.
func (b Bundle) ClientTLSConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(b.ClientCert, b.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b.CACert) {
		return nil, errors.New("invalid CA certificate in bundle")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   b.ServerName,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
