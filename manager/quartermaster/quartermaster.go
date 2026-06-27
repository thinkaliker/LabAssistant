// Package quartermaster owns host provisioning: it mints an enrollment bundle and drives
// the installation of the associate onto a host. Enrollment is asynchronous and reports
// progress through a job; the host flips to online once its associate dials home.
package quartermaster

import (
	"context"

	"github.com/google/uuid"

	"github.com/thinkaliker/labassistant/internal/bundle"
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
	installer   Installer
	managerAddr string
	serverName  string
}

// New builds a Quartermaster.
func New(authority *ca.CA, store *state.Store, jr *jobs.Registry, installer Installer, managerAddr, serverName string) *Quartermaster {
	return &Quartermaster{
		ca: authority, store: store, jobs: jr, installer: installer,
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
	go q.run(context.Background(), hostID, req, job.ID)
	return hostID, job.ID, nil
}

func (q *Quartermaster) run(ctx context.Context, hostID string, req EnrollRequest, jobID string) {
	emit := func(msg string) { q.jobs.AddEvent(jobID, jobs.Event{Kind: "log", Message: msg}) }
	fail := func(err error) {
		emit("error: " + err.Error())
		q.store.SetStatus(hostID, state.StatusError)
		q.jobs.SetResult(jobID, module.JobFailed, nil, err.Error())
	}

	emit("issuing client certificate")
	certPEM, keyPEM, err := q.ca.IssueClient(hostID)
	if err != nil {
		fail(err)
		return
	}
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
}
