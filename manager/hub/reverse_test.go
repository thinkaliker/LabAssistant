package hub

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/thinkaliker/labassistant/associate"
	"github.com/thinkaliker/labassistant/internal/bundle"
	"github.com/thinkaliker/labassistant/manager/ca"
	"github.com/thinkaliker/labassistant/manager/events"
	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/state"
	"github.com/thinkaliker/labassistant/module"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// pingModule is a minimal module: it records that Execute ran and succeeds immediately.
type pingModule struct{ ran chan string }

func (m *pingModule) Manifest() module.Manifest {
	return module.Manifest{
		Name:    "test",
		Version: "0",
		Actions: []module.ActionSpec{{Name: "ping", Privilege: module.PrivilegeNone}},
	}
}
func (m *pingModule) Detect(context.Context) (module.Detection, error) {
	return module.Detection{Applicable: true}, nil
}
func (m *pingModule) Status(context.Context) (module.Status, error) {
	return module.Status{Summary: "ok"}, nil
}
func (m *pingModule) Execute(_ context.Context, req module.ActionRequest, _ func(module.Event)) (module.Result, error) {
	select {
	case m.ran <- req.Action:
	default:
	}
	return module.Result{State: module.JobSucceeded}, nil
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// dispatchAndWait issues a command to a connected host and asserts the module ran and the job
// reached succeeded — proving the full manager->associate->manager round trip.
func dispatchAndWait(t *testing.T, h *Hub, jr *jobs.Registry, mod *pingModule, hostID string) {
	t.Helper()
	job := jr.Create(hostID, "test", "ping", nil)
	if err := h.Dispatch(hostID, &pb.Command{JobId: job.ID, Module: "test", Action: "ping"}); err != nil {
		t.Fatalf("dispatch to %s: %v", hostID, err)
	}
	select {
	case <-mod.ran:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: command never reached the module", hostID)
	}
	waitFor(t, hostID+" job result", 5*time.Second, func() bool {
		j, ok := jr.Get(job.ID)
		return ok && j.Snapshot().State == module.JobSucceeded.String()
	})
}

// TestBothDirections stands up a real CA and exercises a full command round trip in both
// stream directions: dial-home (associate dials the manager) and manager-dial (the manager
// dials the associate), over real mTLS on loopback.
func TestBothDirections(t *testing.T) {
	dir := t.TempDir()
	authority, err := ca.LoadOrCreate(filepath.Join(dir, "certs"), []string{"localhost"})
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	store, err := state.Load(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	jr := jobs.NewRegistry(events.New())
	h := New(store, jr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ---- dial-home: associate dials the manager ----
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(authority.ServerTLSConfig())))
	pb.RegisterManagerServiceServer(srv, h)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	const dhID = "host-dial-home"
	certPEM, keyPEM, _, err := authority.IssueClient(dhID)
	if err != nil {
		t.Fatalf("issue client: %v", err)
	}
	if err := store.Add(state.Host{ID: dhID, Name: "dh", Status: state.StatusOffline}); err != nil {
		t.Fatalf("add dial-home host: %v", err)
	}
	dhMod := &pingModule{ran: make(chan string, 1)}
	dh := associate.New(bundle.Bundle{
		HostID:      dhID,
		ConnMode:    bundle.ModeDialHome,
		ManagerAddr: lis.Addr().String(),
		ServerName:  "localhost",
		CACert:      authority.CAPEM(),
		ClientCert:  certPEM,
		ClientKey:   keyPEM,
	}, dhMod)
	go dh.Run(ctx)
	waitFor(t, "dial-home connect", 5*time.Second, func() bool { return h.Connected(dhID) })
	dispatchAndWait(t, h, jr, dhMod, dhID)

	// ---- manager-dial: the manager dials the associate ----
	// Grab a free port for the associate to listen on, then let the dialer reach it.
	pl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("port probe: %v", err)
	}
	port := pl.Addr().(*net.TCPAddr).Port
	pl.Close()

	const mdID = "host-manager-dial"
	scertPEM, skeyPEM, _, err := authority.IssueServer(mdID, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("issue server: %v", err)
	}
	mdMod := &pingModule{ran: make(chan string, 1)}
	md := associate.New(bundle.Bundle{
		HostID:     mdID,
		ConnMode:   bundle.ModeManagerDial,
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
		CACert:     authority.CAPEM(),
		ClientCert: scertPEM,
		ClientKey:  skeyPEM,
	}, mdMod)
	go md.Run(ctx)

	// Start as "enrolling" (as real enrollment leaves it) to prove the dialer connects it and
	// the associate flips it online, rather than the dialer skipping enrolling hosts.
	if err := store.Add(state.Host{
		ID: mdID, Name: "md", IP: "127.0.0.1", ConnPort: port,
		ConnMode: bundle.ModeManagerDial, Status: state.StatusEnrolling,
	}); err != nil {
		t.Fatalf("add manager-dial host: %v", err)
	}
	dialer := NewDialer(h, store, authority.DialTLSConfig, 200*time.Millisecond)
	dialer.Start(ctx)

	waitFor(t, "manager-dial connect", 8*time.Second, func() bool { return h.Connected(mdID) })
	dispatchAndWait(t, h, jr, mdMod, mdID)
}
