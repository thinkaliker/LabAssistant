// Package quartermaster owns host provisioning: it mints an enrollment bundle and drives
// the installation of the associate onto a host. Enrollment is asynchronous and reports
// progress through a job; the host flips to online once its associate dials home.
package quartermaster

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/thinkaliker/labassistant/internal/bundle"
	"github.com/thinkaliker/labassistant/manager/auditor"
	"github.com/thinkaliker/labassistant/manager/ca"
	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/state"
	"github.com/thinkaliker/labassistant/module"
)

// InstallParams is what an Installer needs to place a running associate on a host.
type InstallParams struct {
	HostID      string
	IP          string
	SSHUser     string
	SSHPassword string
	Bundle      bundle.Bundle
}

// Installer places and starts the associate on a host. emit reports human-readable
// progress lines.
type Installer interface {
	Install(ctx context.Context, p InstallParams, emit func(string)) error
}

// Uninstaller removes the associate from a host. Installers that support SSH/local teardown
// implement it; the quartermaster uses it as the offline fallback for uninstall.
type Uninstaller interface {
	Uninstall(ctx context.Context, p InstallParams, emit func(string)) error
}

// Reviver re-enables and starts an already-installed associate over SSH. Installers that
// support it implement it; the quartermaster uses it to recover a host whose associate did
// not come back after a reboot.
type Reviver interface {
	Revive(ctx context.Context, p InstallParams, emit func(string)) error
}

// EnrollRequest describes a host to add.
type EnrollRequest struct {
	Name        string
	IP          string
	SSHUser     string
	SSHPassword string
	Tailscale   bool
}

// Quartermaster orchestrates enrollment.
type Quartermaster struct {
	ca          *ca.CA
	store       *state.Store
	jobs        *jobs.Registry
	aud         *auditor.Auditor
	installer   Installer
	managerAddr string
	serverName  string

	connected       func(string) bool  // hub liveness check (set via SetStream)
	streamUninstall func(string) error // hub self-uninstall command (set via SetStream)
}

// SetStream wires the hub functions the quartermaster uses for uninstall: connected reports
// whether a host's associate is live, streamUninstall sends the self-uninstall command.
func (q *Quartermaster) SetStream(connected func(string) bool, streamUninstall func(string) error) {
	q.connected = connected
	q.streamUninstall = streamUninstall
}

// UninstallRequest describes a host to remove. SSH credentials are used only for the
// offline teardown fallback and are never persisted.
type UninstallRequest struct {
	HostID      string
	SSHUser     string
	SSHPassword string
}

// ReviveRequest describes a host whose associate should be re-enabled and started over SSH.
// SSH credentials are transient and never persisted.
type ReviveRequest struct {
	HostID      string
	SSHUser     string
	SSHPassword string
}

// New builds a Quartermaster.
func New(authority *ca.CA, store *state.Store, jr *jobs.Registry, aud *auditor.Auditor, installer Installer, managerAddr, serverName string) *Quartermaster {
	return &Quartermaster{
		ca: authority, store: store, jobs: jr, aud: aud, installer: installer,
		managerAddr: managerAddr, serverName: serverName,
	}
}

// Enroll registers a host in the "enrolling" state and starts async provisioning. It
// returns the new host id and the enrollment job id.
func (q *Quartermaster) Enroll(req EnrollRequest) (hostID, jobID string, err error) {
	hostID = uuid.NewString()
	host := state.Host{
		ID: hostID, Name: req.Name, IP: req.IP, SSHUser: req.SSHUser,
		Tailscale: req.Tailscale, Status: state.StatusEnrolling,
	}
	if err := q.store.Add(host); err != nil {
		return "", "", err
	}
	job := q.jobs.Create(hostID, "quartermaster", "enroll", nil)
	q.aud.Record("host_added", hostID, "user", "host added: "+req.Name,
		map[string]string{"name": req.Name, "ip": req.IP})
	go q.run(context.Background(), hostID, req, job.ID)
	return hostID, job.ID, nil
}

// Uninstall removes a host: it tears down the associate (self-uninstall over the stream
// when online, SSH otherwise), revokes the client cert, and drops the host record. Returns
// the progress job id.
func (q *Quartermaster) Uninstall(req UninstallRequest) (jobID string, err error) {
	host, ok := q.store.Get(req.HostID)
	if !ok {
		return "", fmt.Errorf("host not found")
	}
	job := q.jobs.Create(host.ID, "quartermaster", "uninstall", nil)
	go q.runUninstall(context.Background(), host, req, job.ID)
	return job.ID, nil
}

// Revive re-enables and starts the already-installed associate on a host over SSH, to recover
// it after a reboot left the service disabled or dead. Returns the progress job id.
func (q *Quartermaster) Revive(req ReviveRequest) (jobID string, err error) {
	host, ok := q.store.Get(req.HostID)
	if !ok {
		return "", fmt.Errorf("host not found")
	}
	r, ok := q.installer.(Reviver)
	if !ok {
		return "", fmt.Errorf("no SSH revive path available")
	}
	job := q.jobs.Create(host.ID, "quartermaster", "revive", nil)
	go q.runRevive(context.Background(), host, req, r, job.ID)
	return job.ID, nil
}

func (q *Quartermaster) runRevive(ctx context.Context, host state.Host, req ReviveRequest, r Reviver, jobID string) {
	emit := func(msg string) { q.jobs.AddEvent(jobID, jobs.Event{Kind: "log", Message: msg}) }

	if q.connected != nil && q.connected(host.ID) {
		emit("host is already online; nothing to revive")
		q.jobs.SetResult(jobID, module.JobSucceeded, nil, "")
		return
	}
	emit("reviving associate on " + host.IP + " over SSH")
	err := r.Revive(ctx, InstallParams{
		HostID: host.ID, IP: host.IP, SSHUser: req.SSHUser, SSHPassword: req.SSHPassword,
	}, emit)
	if err != nil {
		emit("error: " + err.Error())
		q.jobs.SetResult(jobID, module.JobFailed, nil, err.Error())
		q.aud.Record("host_revive_failed", host.ID, "user", "associate revive failed: "+err.Error(), nil)
		return
	}
	emit("associate started; awaiting connection")
	q.jobs.SetResult(jobID, module.JobSucceeded, nil, "")
	q.aud.Record("host_revived", host.ID, "user", "associate revived over SSH", nil)
}

func (q *Quartermaster) runUninstall(ctx context.Context, host state.Host, req UninstallRequest, jobID string) {
	emit := func(msg string) { q.jobs.AddEvent(jobID, jobs.Event{Kind: "log", Message: msg}) }

	var teardownErr error
	switch {
	case q.connected != nil && q.connected(host.ID) && q.streamUninstall != nil:
		emit("host online; sending self-uninstall over stream")
		if teardownErr = q.streamUninstall(host.ID); teardownErr == nil {
			emit("associate is tearing itself down")
		}
	default:
		if u, ok := q.installer.(Uninstaller); ok {
			emit("host offline; removing associate over SSH")
			teardownErr = u.Uninstall(ctx, InstallParams{
				HostID: host.ID, IP: host.IP, SSHUser: req.SSHUser, SSHPassword: req.SSHPassword,
			}, emit)
		} else {
			emit("no remote teardown path available; removing host record only")
		}
	}

	// Remove the host and revoke its cert regardless of teardown outcome — the operator
	// asked to remove it. A failed teardown leaves an orphaned (cert-revoked) associate the
	// operator can clean up later over SSH.
	if host.CertSerial != "" {
		q.ca.Revoke(host.CertSerial)
		q.aud.Record("cert_revoked", host.ID, "user", "client certificate revoked", nil)
	}
	_ = q.store.Remove(host.ID)
	q.aud.Record("host_removed", host.ID, "user", "host uninstalled", nil)

	if teardownErr != nil {
		emit("warning: remote teardown failed: " + teardownErr.Error())
		q.jobs.SetResult(jobID, module.JobSucceeded, nil, "host removed; remote teardown error: "+teardownErr.Error())
		return
	}
	emit("host removed")
	q.jobs.SetResult(jobID, module.JobSucceeded, nil, "")
}

func (q *Quartermaster) run(ctx context.Context, hostID string, req EnrollRequest, jobID string) {
	emit := func(msg string) { q.jobs.AddEvent(jobID, jobs.Event{Kind: "log", Message: msg}) }
	fail := func(err error) {
		emit("error: " + err.Error())
		q.store.SetStatus(hostID, state.StatusError)
		q.jobs.SetResult(jobID, module.JobFailed, nil, err.Error())
		q.aud.Record("host_enroll_failed", hostID, "manager", "enrollment failed: "+err.Error(), nil)
	}

	emit("issuing client certificate")
	certPEM, keyPEM, serial, err := q.ca.IssueClient(hostID)
	if err != nil {
		fail(err)
		return
	}
	expiry := time.Now().Add(q.ca.LeafValidity())
	q.store.Edit(hostID, func(h *state.Host) {
		h.CertSerial = serial
		h.CertExpiry = expiry
	})
	q.aud.Record("cert_issued", hostID, "manager", "client certificate issued", nil)
	b := bundle.Bundle{
		HostID:      hostID,
		ManagerAddr: q.managerAddr,
		ServerName:  q.serverName,
		CACert:      q.ca.CAPEM(),
		ClientCert:  certPEM,
		ClientKey:   keyPEM,
	}

	emit("installing associate on " + req.IP)
	params := InstallParams{
		HostID: hostID, IP: req.IP, SSHUser: req.SSHUser,
		SSHPassword: req.SSHPassword, Bundle: b,
	}
	if err := q.installer.Install(ctx, params, emit); err != nil {
		fail(err)
		return
	}

	emit("associate installed; awaiting connection")
	q.jobs.SetResult(jobID, module.JobSucceeded, nil, "")
	q.aud.Record("host_enroll_complete", hostID, "manager", "associate installed", nil)
}
