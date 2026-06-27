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
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

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
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// App holds the manager's wired subsystems.
type App struct {
	cfg       config.Config
	ca        *ca.CA
	store     *state.Store
	jobs      *jobs.Registry
	events    *events.Broker
	hub       *hub.Hub
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
	authority, err := ca.LoadOrCreate(layout.CertsDir(), nil)
	if err != nil {
		return nil, err
	}
	store, err := state.Load(layout.StateFile())
	if err != nil {
		return nil, err
	}
	ev := events.New()
	store.SetNotify(func(c state.Change) {
		ev.Publish(envelope("host", c))
	})
	jr := jobs.NewRegistry(ev)

	aud, err := auditor.Open(layout.AuditFile(), cfg.Audit.Max, func(e auditor.Entry) {
		ev.Publish(envelope("audit", e))
	})
	if err != nil {
		return nil, err
	}
	jr.SetResultHook(func(j jobs.View) {
		aud.Record("job_"+j.State, j.HostID, "manager",
			fmt.Sprintf("%s %s %s", j.Module, j.Action, j.State),
			map[string]string{"jobId": j.ID})
	})

	var installer quartermaster.Installer
	switch cfg.Enroll.Mode {
	case "ssh":
		installer = quartermaster.SSHInstaller{
			AssociateBin:   cfg.Enroll.AssociateBin,
			HelperBin:      cfg.Enroll.HelperBin,
			KnownHostsPath: layout.KnownHostsFile(),
		}
	default:
		installer = quartermaster.LocalInstaller{
			AssociateBin: cfg.Enroll.AssociateBin,
			HelperBin:    cfg.Enroll.HelperBin,
			WorkDir:      filepath.Join(layout.Data, "hosts"),
		}
	}
	qm := quartermaster.New(authority, store, jr, aud, installer, cfg.Enroll.ManagerAddr, cfg.Enroll.ServerName)

	h := hub.New(store, jr)
	runner := actions.NewRunner(store, jr, h, ev, aud)
	sched, err := scheduler.Load(
		layout.TasksFile(),
		func(hostID, moduleName, action string, params json.RawMessage) error {
			_, err := runner.Run(hostID, moduleName, action, params, true)
			return err
		},
		h.Connected,
		func() { ev.Publish(envelope("task_changed", nil)) },
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
		cfg:       cfg,
		ca:        authority,
		store:     store,
		jobs:      jr,
		events:    ev,
		hub:       h,
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
	go a.scheduler.Start(ctx)
	go a.certRotationLoop(ctx)

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

// rotateCert issues a fresh client certificate for a connected host and pushes it down the
// stream. The old certificate is left to expire (not revoked) to avoid locking the host out
// if the new cert never lands.
func (a *App) rotateCert(hostID string) error {
	if _, ok := a.store.Get(hostID); !ok {
		return fmt.Errorf("host not found")
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
func (a *App) certRotationLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, h := range a.store.Hosts() {
				if h.CertExpiry.IsZero() || !a.hub.Connected(h.ID) {
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

func envelope(typ string, payload any) []byte {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{typ, payload})
	return b
}
