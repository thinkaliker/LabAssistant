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
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/thinkaliker/labassistant/internal/paths"
	"github.com/thinkaliker/labassistant/manager/ca"
	"github.com/thinkaliker/labassistant/manager/config"
	"github.com/thinkaliker/labassistant/manager/events"
	"github.com/thinkaliker/labassistant/manager/hub"
	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/state"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// App holds the manager's wired subsystems.
type App struct {
	cfg    config.Config
	ca     *ca.CA
	store  *state.Store
	jobs   *jobs.Registry
	events *events.Broker
	hub    *hub.Hub
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
	return &App{
		cfg:    cfg,
		ca:     authority,
		store:  store,
		jobs:   jr,
		events: ev,
		hub:    hub.New(store, jr),
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

func envelope(typ string, payload any) []byte {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{typ, payload})
	return b
}
