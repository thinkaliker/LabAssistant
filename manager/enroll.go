package manager

import (
	"net"

	"github.com/google/uuid"

	"github.com/thinkaliker/labassistant/internal/bundle"
	"github.com/thinkaliker/labassistant/internal/paths"
	"github.com/thinkaliker/labassistant/manager/ca"
	"github.com/thinkaliker/labassistant/manager/state"
)

// Enroll registers a new host and mints an enrollment bundle for its associate. This is
// the Slice 1 dev path (the bundle is copied to the host by hand); the quartermaster will
// deliver it over SSH in a later slice.
func Enroll(layout paths.Layout, name, ip, managerAddr, serverName string) (bundle.Bundle, error) {
	sans := []string{serverName}
	if host, _, err := net.SplitHostPort(managerAddr); err == nil && host != "" {
		sans = append(sans, host)
	}
	authority, err := ca.LoadOrCreate(layout.CertsDir(), sans)
	if err != nil {
		return bundle.Bundle{}, err
	}
	store, err := state.Load(layout.StateFile())
	if err != nil {
		return bundle.Bundle{}, err
	}

	hostID := uuid.NewString()
	certPEM, keyPEM, serial, err := authority.IssueClient(hostID)
	if err != nil {
		return bundle.Bundle{}, err
	}
	if err := store.Add(state.Host{ID: hostID, Name: name, IP: ip, CertSerial: serial}); err != nil {
		return bundle.Bundle{}, err
	}
	return bundle.Bundle{
		HostID:      hostID,
		ManagerAddr: managerAddr,
		ServerName:  serverName,
		CACert:      authority.CAPEM(),
		ClientCert:  certPEM,
		ClientKey:   keyPEM,
	}, nil
}
