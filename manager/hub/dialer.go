package hub

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/thinkaliker/labassistant/internal/bundle"
	"github.com/thinkaliker/labassistant/manager/state"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// defaultDialBackoff is how long the dialer waits before retrying a manager-dial host after
// its stream ends or a dial fails.
const defaultDialBackoff = 5 * time.Second

// Dialer maintains the manager's outbound streams to manager-dial hosts. For each such host it
// runs a goroutine that dials the associate, opens the Attach stream, and serves it through the
// hub (so dispatch, log streams, and liveness work identically to dial-home). It reconciles the
// set of running dialers against the host registry via Sync.
type Dialer struct {
	hub     *Hub
	store   *state.Store
	tlsFor  func(hostID string) *tls.Config // manager client TLS pinned to the host id
	backoff time.Duration

	mu     sync.Mutex
	parent context.Context
	active map[string]*handle
}

// handle tracks one running host dialer: its cancel func and the address it dials (so Sync can
// restart it if the address changed).
type handle struct {
	cancel context.CancelFunc
	addr   string
}

// NewDialer builds a Dialer. tlsFor returns the TLS config used to dial a given host (typically
// ca.DialTLSConfig, which pins the associate's certificate CommonName to the host id).
func NewDialer(h *Hub, store *state.Store, tlsFor func(hostID string) *tls.Config, backoff time.Duration) *Dialer {
	if backoff <= 0 {
		backoff = defaultDialBackoff
	}
	return &Dialer{hub: h, store: store, tlsFor: tlsFor, backoff: backoff, active: map[string]*handle{}}
}

// Start records the parent context, performs an initial Sync, and stops all dialers when the
// context ends. It does not block.
func (d *Dialer) Start(ctx context.Context) {
	d.mu.Lock()
	d.parent = ctx
	d.mu.Unlock()
	d.Sync()
	go func() {
		<-ctx.Done()
		d.stopAll()
	}()
}

// Sync reconciles running dialers with the store: it starts a dialer for every manager-dial
// host that has a dial address and is past enrollment, stops dialers for hosts that no longer
// qualify, and restarts a dialer whose address changed. It is safe to call on every host change
// and is a no-op until Start has recorded the parent context.
func (d *Dialer) Sync() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.parent == nil {
		return
	}

	// A manager-dial host stays "enrolling" until the dialer first connects it (the associate
	// flips it online on connect, exactly as dial-home does), so enrolling hosts must be dialed
	// too — the dial loop retries harmlessly with backoff until the associate is up.
	want := map[string]string{} // hostID -> dial addr
	for _, h := range d.store.Hosts() {
		if h.ConnMode != bundle.ModeManagerDial {
			continue
		}
		if addr := dialAddr(h); addr != "" {
			want[h.ID] = addr
		}
	}

	// Stop dialers no longer wanted, or whose address changed (restarted below).
	for id, hd := range d.active {
		if addr, ok := want[id]; !ok || addr != hd.addr {
			hd.cancel()
			delete(d.active, id)
		}
	}
	// Start missing dialers.
	for id, addr := range want {
		if _, ok := d.active[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(d.parent)
		d.active[id] = &handle{cancel: cancel, addr: addr}
		go d.dialLoop(ctx, id, addr)
	}
}

func (d *Dialer) stopAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, hd := range d.active {
		hd.cancel()
		delete(d.active, id)
	}
}

// dialLoop dials one host and serves its stream, retrying with backoff until ctx ends.
func (d *Dialer) dialLoop(ctx context.Context, hostID, addr string) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := d.dialOnce(ctx, hostID, addr); err != nil && ctx.Err() == nil {
			slog.Debug("manager-dial stream ended; retrying", "host", hostID, "addr", addr, "err", err, "in", d.backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.backoff):
		}
	}
}

func (d *Dialer) dialOnce(ctx context.Context, hostID, addr string) error {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(credentials.NewTLS(d.tlsFor(hostID))),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                20 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := pb.NewAssociateServiceClient(conn).Attach(sctx)
	if err != nil {
		return err
	}
	return d.hub.serveStream(sctx, hostID, stream)
}

// dialAddr is the address the manager dials for a manager-dial host, or "" if it is not yet
// known (missing IP or port).
func dialAddr(h state.Host) string {
	if h.IP == "" || h.ConnPort == 0 {
		return ""
	}
	return net.JoinHostPort(h.IP, strconv.Itoa(h.ConnPort))
}
