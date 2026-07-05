// Package quartermaster owns host provisioning: it mints an enrollment bundle and drives
// the installation of the associate onto a host. Enrollment is asynchronous and reports
// progress through a job; the host flips to online once its associate dials home.
package quartermaster

import (
	"context"
	"fmt"
	"log/slog"
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
	// ConnMode selects the stream direction: bundle.ModeDialHome (default) or
	// bundle.ModeManagerDial. ConnPort overrides the default listen port in manager-dial mode.
	ConnMode string
	ConnPort int
}

// Mode names for how an associate is installed on a host.
const (
	ModeLocal = "local" // associate runs as a child process on the manager box
	ModeSSH   = "ssh"   // associate installed on a remote host over SSH
)

// modeFor picks the install mode from an enroll request: a host with SSH
// credentials is remote (ssh); one without is the manager box itself (local).
func modeFor(sshUser string) string {
	if sshUser == "" {
		return ModeLocal
	}
	return ModeSSH
}

// resolveConn normalizes the requested connection direction and listen port. Manager-dial
// hosts get the requested port or the configured default; dial-home hosts get no listen port.
func (q *Quartermaster) resolveConn(req EnrollRequest) (mode string, port int) {
	if req.ConnMode == bundle.ModeManagerDial {
		port = req.ConnPort
		if port == 0 {
			port = q.associatePort
		}
		return bundle.ModeManagerDial, port
	}
	return bundle.ModeDialHome, 0
}

// Quartermaster orchestrates enrollment.
type Quartermaster struct {
	ca            *ca.CA
	store         *state.Store
	jobs          *jobs.Registry
	aud           *auditor.Auditor
	local         Installer // installs on the manager box (child process)
	ssh           Installer // installs on remote hosts over SSH
	managerAddr   string
	serverName    string
	associatePort int // default listen port for manager-dial hosts

	connected       func(string) bool  // hub liveness check (set via SetStream)
	streamUninstall func(string) error // hub self-uninstall command (set via SetStream)
}

// installerFor returns the installer for a host's mode. An empty mode (hosts
// enrolled before per-host mode existed) is inferred from whether the host has
// an SSH user.
func (q *Quartermaster) installerFor(mode, sshUser string) Installer {
	if mode == "" {
		mode = modeFor(sshUser)
	}
	if mode == ModeLocal {
		return q.local
	}
	return q.ssh
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

// New builds a Quartermaster. local installs the associate as a child process on the
// manager box; ssh installs it on remote hosts. The mode is chosen per host at enroll.
// associatePort is the default listen port baked into manager-dial bundles.
func New(authority *ca.CA, store *state.Store, jr *jobs.Registry, aud *auditor.Auditor, local, ssh Installer, managerAddr, serverName string, associatePort int) *Quartermaster {
	if associatePort == 0 {
		associatePort = 8444
	}
	return &Quartermaster{
		ca: authority, store: store, jobs: jr, aud: aud, local: local, ssh: ssh,
		managerAddr: managerAddr, serverName: serverName, associatePort: associatePort,
	}
}

// Enroll registers a host in the "enrolling" state and starts async provisioning. It
// returns the new host id and the enrollment job id.
func (q *Quartermaster) Enroll(req EnrollRequest) (hostID, jobID string, err error) {
	connMode, connPort := q.resolveConn(req)
	hostID = uuid.NewString()
	host := state.Host{
		ID: hostID, Name: req.Name, IP: req.IP, SSHUser: req.SSHUser,
		Mode: modeFor(req.SSHUser), Tailscale: req.Tailscale, Status: state.StatusEnrolling,
		ConnMode: connMode, ConnPort: connPort,
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
	r, ok := q.installerFor(host.Mode, host.SSHUser).(Reviver)
	if !ok {
		return "", fmt.Errorf("no revive path available for this host")
	}
	job := q.jobs.Create(host.ID, "quartermaster", "revive", nil)
	go q.runRevive(context.Background(), host, req, r, job.ID)
	return job.ID, nil
}

// ReviveLocalOrphans restarts local-mode associates whose child process is gone. Local
// associates are children of the manager process, so a manager restart (e.g. `manage
// update`) takes them down with it; this brings them back on the next boot without an
// operator having to click Revive. Reviving an already-running child is a no-op.
func (q *Quartermaster) ReviveLocalOrphans(ctx context.Context) {
	r, ok := q.local.(Reviver)
	if !ok {
		return
	}
	for _, host := range q.store.Hosts() {
		if host.Mode != ModeLocal {
			continue
		}
		emit := func(msg string) { slog.Info("revive local associate", "host", host.Name, "msg", msg) }
		if err := r.Revive(ctx, InstallParams{HostID: host.ID}, emit); err != nil {
			slog.Warn("revive local associate failed", "host", host.Name, "err", err)
			continue
		}
		q.aud.Record("host_revived", host.ID, "manager", "local associate revived on manager start", nil)
	}
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
		if u, ok := q.installerFor(host.Mode, host.SSHUser).(Uninstaller); ok {
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

	connMode, connPort := q.resolveConn(req)
	var (
		certPEM, keyPEM []byte
		serial          string
		err             error
	)
	if connMode == bundle.ModeManagerDial {
		emit("issuing server certificate")
		certPEM, keyPEM, serial, err = q.ca.IssueServer(hostID, []string{req.IP})
	} else {
		emit("issuing client certificate")
		certPEM, keyPEM, serial, err = q.ca.IssueClient(hostID)
	}
	if err != nil {
		fail(err)
		return
	}
	expiry := time.Now().Add(q.ca.LeafValidity())
	q.store.Edit(hostID, func(h *state.Host) {
		h.CertSerial = serial
		h.CertExpiry = expiry
	})
	q.aud.Record("cert_issued", hostID, "manager", "certificate issued", nil)

	b := bundle.Bundle{
		HostID:      hostID,
		ConnMode:    connMode,
		ManagerAddr: q.managerAddr,
		ServerName:  q.serverName,
		CACert:      q.ca.CAPEM(),
		ClientCert:  certPEM,
		ClientKey:   keyPEM,
	}
	if connMode == bundle.ModeManagerDial {
		b.ListenAddr = fmt.Sprintf(":%d", connPort)
		emit(fmt.Sprintf("manager-dial mode: the manager will dial %s:%d — ensure the host firewall allows inbound TCP %d from the manager", req.IP, connPort, connPort))
	}

	emit("installing associate on " + req.IP)
	params := InstallParams{
		HostID: hostID, IP: req.IP, SSHUser: req.SSHUser,
		SSHPassword: req.SSHPassword, Bundle: b,
	}
	if err := q.installerFor(modeFor(req.SSHUser), req.SSHUser).Install(ctx, params, emit); err != nil {
		fail(err)
		return
	}

	emit("associate installed; awaiting connection")
	q.jobs.SetResult(jobID, module.JobSucceeded, nil, "")
	q.aud.Record("host_enroll_complete", hostID, "manager", "associate installed", nil)
}
