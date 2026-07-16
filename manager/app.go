// Package manager wires the manager's subsystems together: the CA, host registry, job
// registry, event broker, gRPC hub, and HTTP server (REST API + embedded dashboard).
package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/thinkaliker/labassistant/internal/bundle"
	"github.com/thinkaliker/labassistant/internal/paths"
	"github.com/thinkaliker/labassistant/manager/actions"
	"github.com/thinkaliker/labassistant/manager/api"
	"github.com/thinkaliker/labassistant/manager/auditor"
	"github.com/thinkaliker/labassistant/manager/ca"
	"github.com/thinkaliker/labassistant/manager/config"
	"github.com/thinkaliker/labassistant/manager/events"
	"github.com/thinkaliker/labassistant/manager/hub"
	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/modconfig"
	"github.com/thinkaliker/labassistant/manager/quartermaster"
	"github.com/thinkaliker/labassistant/manager/scheduler"
	"github.com/thinkaliker/labassistant/manager/settings"
	"github.com/thinkaliker/labassistant/manager/state"
	"github.com/thinkaliker/labassistant/module"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// App holds the manager's wired subsystems.
type App struct {
	cfg       config.Config
	layout    paths.Layout
	instance  string
	ca        *ca.CA
	store     *state.Store
	jobs      *jobs.Registry
	events    *events.Broker
	hub       *hub.Hub
	dialer    *hub.Dialer
	qm        *quartermaster.Quartermaster
	runner    *actions.Runner
	scheduler *scheduler.Scheduler
	aud       *auditor.Auditor
	settings  *settings.Store
	modconfig *modconfig.Store
	sessions  *api.Sessions
	backup    *api.Backup
}

// NewApp builds the manager from its on-disk layout and configuration.
func NewApp(layout paths.Layout, cfg config.Config) (*App, error) {
	authority, err := ca.LoadOrCreate(layout.CertsDir(), serverSANs(cfg))
	if err != nil {
		return nil, err
	}
	store, err := state.Load(layout.StateFile())
	if err != nil {
		return nil, err
	}
	ev := events.New()
	// dialer is assigned below once the hub exists; the notify closure reconciles the
	// manager-dial pool on every host change (add/remove/edit) via the same variable.
	var dialer *hub.Dialer
	store.SetNotify(func(c state.Change) {
		ev.Publish(envelope("host", c))
		if dialer != nil {
			dialer.Sync()
		}
	})
	jr := jobs.NewRegistry(ev)

	aud, err := auditor.Open(layout.AuditFile(), cfg.Audit.Max, func(e auditor.Entry) {
		ev.Publish(envelope("audit", e))
	})
	if err != nil {
		return nil, err
	}
	// Default the deploy binaries to siblings of the running manager executable
	// (a standard deploy builds bin/manager, bin/associate, bin/associatehelper
	// together) so enrollment works without setting enroll.associate_bin by hand.
	assocBin, helperBin := cfg.Enroll.AssociateBin, cfg.Enroll.HelperBin
	if exe, err := os.Executable(); err == nil {
		binDir := filepath.Dir(exe)
		if assocBin == "" {
			assocBin = filepath.Join(binDir, "associate")
		}
		if helperBin == "" {
			if p := filepath.Join(binDir, "associatehelper"); fileExists(p) {
				helperBin = p
			}
		}
	}
	// Both installers are always available; the quartermaster picks per host —
	// local (child process on this box) for a host added without SSH credentials,
	// ssh for one with them. This lets one manager cover itself and remote hosts.
	localInstaller := quartermaster.LocalInstaller{
		AssociateBin: assocBin,
		HelperBin:    helperBin,
		WorkDir:      filepath.Join(layout.Data, "hosts"),
	}
	sshInstaller := quartermaster.SSHInstaller{
		AssociateBin:   assocBin,
		HelperBin:      helperBin,
		KnownHostsPath: layout.KnownHostsFile(),
	}
	qm := quartermaster.New(authority, store, jr, aud, localInstaller, sshInstaller, cfg.Enroll.ManagerAddr, cfg.Enroll.ServerName, cfg.Enroll.AssociatePort)

	h := hub.New(store, jr)
	dialer = hub.NewDialer(h, store, authority.DialTLSConfig, 0)
	qm.SetStream(h.Connected, h.Uninstall)
	runner := actions.NewRunner(store, jr, h, ev, aud)
	jr.SetResultHook(func(j jobs.View) {
		aud.Record("job_"+j.State, j.HostID, "manager",
			fmt.Sprintf("%s %s %s", j.Module, j.Action, j.State),
			map[string]string{"jobId": j.ID})
		// An elevated action that needs a sudo password pauses here: surface a prompt the
		// dashboard can answer, then re-dispatch with the supplied password.
		if j.State == module.JobNeedsSudoPassword.String() {
			runner.SudoRequired(j.ID, j.HostID, j.Module, j.Action, j.Params)
		}
	})
	sched, err := scheduler.Load(
		layout.TasksFile(),
		func(hostID, moduleName, action string, params json.RawMessage) error {
			_, err := runner.Run(hostID, moduleName, action, params, true)
			return err
		},
		h.Connected,
		func() { ev.Publish(envelope("task_changed", nil)) },
		func(t scheduler.Task, hostID string, err error) {
			if err == nil {
				return
			}
			summary := "scheduled " + t.Module + "/" + t.Action + " failed: " + err.Error()
			aud.Record("task_run_failed", hostID, "scheduler", summary,
				map[string]string{"task": t.Name, "taskId": t.ID})
			ev.Publish(envelope("task_run_failed", map[string]string{
				"taskId": t.ID, "name": t.Name, "hostId": hostID, "error": err.Error(),
			}))
		},
	)
	if err != nil {
		return nil, err
	}

	set, err := settings.Load(layout.SettingsFile())
	if err != nil {
		return nil, err
	}
	mc, err := modconfig.Load(layout.ModConfigFile())
	if err != nil {
		return nil, err
	}

	return &App{
		cfg:    cfg,
		layout: layout,
		// instance changes every process start; the dashboard polls it to notice when the
		// manager restarted underneath it (e.g. after a self-update) and prompt re-login.
		instance:  fmt.Sprintf("%d", time.Now().UnixNano()),
		ca:        authority,
		store:     store,
		jobs:      jr,
		events:    ev,
		hub:       h,
		dialer:    dialer,
		qm:        qm,
		runner:    runner,
		scheduler: sched,
		aud:       aud,
		settings:  set,
		modconfig: mc,
		sessions:  api.NewSessions(),
		backup:    &api.Backup{Layout: layout},
	}, nil
}

// Serve starts the gRPC and HTTP servers and blocks until ctx is cancelled.
func (a *App) Serve(ctx context.Context) error {
	grpcSrv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(a.ca.ServerTLSConfig())),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    25 * time.Second,
			Timeout: 10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	pb.RegisterManagerServiceServer(grpcSrv, a.hub)

	grpcLis, err := net.Listen("tcp", a.cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen grpc %s: %w", a.cfg.GRPCAddr, err)
	}
	httpSrv := &http.Server{Addr: a.cfg.HTTPAddr, Handler: a.httpHandler()}

	errCh := make(chan error, 2)
	go func() {
		slog.Info("grpc listening", "addr", a.cfg.GRPCAddr)
		errCh <- grpcSrv.Serve(grpcLis)
	}()
	go func() {
		slog.Info("http listening", "addr", a.cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	a.dialer.Start(ctx)
	go a.scheduler.Start(ctx)
	go a.certRotationLoop(ctx)
	go a.jobReaperLoop(ctx)
	// Local associates run as children of this process, so a manager restart (e.g.
	// `manage update`) kills them. Bring any that are down back up on start.
	go a.qm.ReviveLocalOrphans(ctx)

	select {
	case <-ctx.Done():
		grpcSrv.GracefulStop()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		grpcSrv.Stop()
		_ = httpSrv.Close()
		return err
	}
}

// selfUpdate runs `scripts/manage.sh update` (git pull, rebuild, restart) for the manager on
// its own host. It is spawned detached in a new session so the pull+build run to completion
// independently of this process; the script's final `systemctl restart` then tears this
// manager down on purpose, which is why the dashboard must re-login afterwards.
// updateEnv returns the manager's environment with the Go toolchain's bin dir prepended to
// PATH, so a self-update spawned under systemd's minimal PATH can still find `go`. Candidates
// cover the GOROOT this binary was built with plus the common install locations.
func updateEnv() []string {
	env := os.Environ()
	dirs := []string{}
	if goroot := runtime.GOROOT(); goroot != "" {
		dirs = append(dirs, filepath.Join(goroot, "bin"))
	}
	dirs = append(dirs, "/usr/local/go/bin")
	if home := os.Getenv("HOME"); home != "" {
		dirs = append(dirs, filepath.Join(home, "go", "bin"))
	}

	path := os.Getenv("PATH")
	var add []string
	for _, d := range dirs {
		if fileExists(filepath.Join(d, "go")) && !pathContains(path, d) {
			add = append(add, d)
		}
	}
	if len(add) == 0 {
		return env
	}
	newPath := strings.Join(add, string(os.PathListSeparator)) + string(os.PathListSeparator) + path

	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PATH="+newPath)
}

// pathContains reports whether dir is already an entry in a PATH-style list.
func pathContains(list, dir string) bool {
	for _, p := range strings.Split(list, string(os.PathListSeparator)) {
		if p == dir {
			return true
		}
	}
	return false
}

// updateLogPath is where selfUpdate streams the update script's output. The log-tail SSE
// endpoint reads the same file so the dashboard can surface progress in the jobs panel.
func (a *App) updateLogPath() string {
	return filepath.Join(a.layout.Data, "manager-update.log")
}

func (a *App) selfUpdate() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// A standard deploy builds bin/manager inside the checkout, so the repo root is two
	// levels up and scripts/manage.sh drives the update lifecycle.
	checkout := filepath.Dir(filepath.Dir(exe))
	script := filepath.Join(checkout, "scripts", "manage.sh")
	if !fileExists(script) {
		return fmt.Errorf("update script not found at %s", script)
	}
	logPath := a.updateLogPath()
	logf, err := os.Create(logPath)
	if err != nil {
		return err
	}
	cmd := exec.Command("bash", script, "update")
	cmd.Dir = checkout
	cmd.Stdout = logf
	cmd.Stderr = logf
	// systemd hands the manager a minimal PATH that usually omits the Go toolchain
	// (/usr/local/go/bin), so the detached build can't find `go`. Add the toolchain dir back.
	cmd.Env = updateEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logf.Close()
		return err
	}
	slog.Info("manager self-update started", "script", script, "log", logPath)
	a.aud.Record("manager_update", "", "manager", "manager self-update triggered from dashboard", nil)
	go func() { _ = cmd.Wait(); logf.Close() }()
	return nil
}

// rotateCert issues a fresh client certificate for a connected host and pushes it down the
// stream. The old certificate is left to expire (not revoked) to avoid locking the host out
// if the new cert never lands.
func (a *App) rotateCert(hostID string) error {
	host, ok := a.store.Get(hostID)
	if !ok {
		return fmt.Errorf("host not found")
	}
	// Rotation pushes a client certificate down the stream; manager-dial associates present a
	// server certificate instead, so this path does not apply to them (re-enroll to rotate).
	if host.ConnMode == bundle.ModeManagerDial {
		return fmt.Errorf("cert rotation not supported for manager-dial hosts")
	}
	if !a.hub.Connected(hostID) {
		return fmt.Errorf("host not connected")
	}
	certPEM, keyPEM, serial, err := a.ca.IssueClient(hostID)
	if err != nil {
		return err
	}
	if err := a.hub.RotateCert(hostID, certPEM, keyPEM); err != nil {
		return err
	}
	expiry := time.Now().Add(a.ca.LeafValidity())
	a.store.Edit(hostID, func(h *state.Host) {
		h.CertSerial = serial
		h.CertExpiry = expiry
	})
	a.aud.Record("cert_rotated", hostID, "manager", "client certificate rotated", nil)
	return nil
}

const certRenewThreshold = 30 * 24 * time.Hour

// certRotationLoop periodically rotates certificates that are near expiry.
// jobReaperLoop periodically drops long-finished jobs so the in-memory registry doesn't grow
// without bound over the manager's lifetime. The retention window keeps recently-settled jobs
// around long enough for the dashboard to display and recover them after a refresh.
func (a *App) jobReaperLoop(ctx context.Context) {
	const retain = time.Hour
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := a.jobs.Prune(retain); n > 0 {
				slog.Debug("pruned finished jobs", "count", n)
			}
		}
	}
}

func (a *App) certRotationLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, h := range a.store.Hosts() {
				// Manager-dial hosts use server certs rotated only by re-enrollment.
				if h.ConnMode == bundle.ModeManagerDial || h.CertExpiry.IsZero() || !a.hub.Connected(h.ID) {
					continue
				}
				if time.Until(h.CertExpiry) < certRenewThreshold {
					if err := a.rotateCert(h.ID); err != nil {
						slog.Warn("cert rotation failed", "host", h.ID, "err", err)
					}
				}
			}
		}
	}
}

// serverSANs collects the names/IPs the manager's server cert must cover so associates
// can verify it: the configured ServerName plus the host part of ManagerAddr.
func serverSANs(cfg config.Config) []string {
	var sans []string
	if cfg.Enroll.ServerName != "" {
		sans = append(sans, cfg.Enroll.ServerName)
	}
	if host, _, err := net.SplitHostPort(cfg.Enroll.ManagerAddr); err == nil && host != "" {
		sans = append(sans, host)
	}
	return sans
}

func envelope(typ string, payload any) []byte {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{typ, payload})
	return b
}

// fileExists reports whether path exists (used to auto-detect the optional helper binary).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
