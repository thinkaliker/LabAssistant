// Package associate is the per-host agent runtime: it dials home to the manager over a
// persistent mTLS stream, advertises its compiled-in modules, runs a heartbeat, and
// serializes incoming commands through a per-host queue.
package associate

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/thinkaliker/labassistant/internal/bundle"
	"github.com/thinkaliker/labassistant/module"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

const (
	// ProtocolVersion is the wire protocol version the associate speaks.
	ProtocolVersion = 1

	heartbeatInterval = 30 * time.Second
	reconnectBackoff  = 5 * time.Second
)

// Associate is the agent runtime.
type Associate struct {
	bundleMu   sync.Mutex
	bundle     bundle.Bundle
	bundlePath string // where to persist a rotated cert
	modules    map[string]module.Module
	order      []string

	helperPath string // path to the privileged helper binary; empty = run elevated actions in-process
	useSudo    bool   // invoke the helper via sudo

	sudoMu     sync.Mutex
	sudoPass   string    // last accepted sudo password, cached to avoid re-prompting every action
	sudoExpiry time.Time // when the cached password lapses (mirrors sudo's own timestamp)
}

// sudoCacheTTL bounds how long an accepted sudo password is reused before the operator is
// prompted again, mirroring sudo's default credential timestamp.
const sudoCacheTTL = 5 * time.Minute

// cachedSudo returns the cached sudo password if still valid, else "".
func (a *Associate) cachedSudo() string {
	a.sudoMu.Lock()
	defer a.sudoMu.Unlock()
	if a.sudoPass == "" || time.Now().After(a.sudoExpiry) {
		return ""
	}
	return a.sudoPass
}

// cacheSudo records a password that sudo just accepted, refreshing its TTL.
func (a *Associate) cacheSudo(pw string) {
	a.sudoMu.Lock()
	a.sudoPass = pw
	a.sudoExpiry = time.Now().Add(sudoCacheTTL)
	a.sudoMu.Unlock()
}

// SetBundlePath sets where the associate persists a rotated certificate.
func (a *Associate) SetBundlePath(path string) { a.bundlePath = path }

// clientTLS builds the current client TLS config under lock.
func (a *Associate) clientTLS() (*tls.Config, error) {
	a.bundleMu.Lock()
	defer a.bundleMu.Unlock()
	return a.bundle.ClientTLSConfig()
}

// applyRotatedCert persists a new client cert/key and returns whether it changed.
func (a *Associate) applyRotatedCert(certPEM, keyPEM []byte) error {
	a.bundleMu.Lock()
	defer a.bundleMu.Unlock()
	a.bundle.ClientCert = certPEM
	a.bundle.ClientKey = keyPEM
	if a.bundlePath != "" {
		return a.bundle.Save(a.bundlePath)
	}
	return nil
}

// SetHelper configures the privileged helper used for elevated actions. path is the
// associatehelper binary (empty leaves elevated actions running in-process); useSudo
// runs it via sudo.
func (a *Associate) SetHelper(path string, useSudo bool) {
	a.helperPath = path
	a.useSudo = useSudo
}

// helperCommand builds the argv for one elevated invocation, or nil when no helper is
// configured (the caller then runs the action in-process). When sudo is in use, a non-empty
// password selects `sudo -S` (password read from the first line of stdin) and an empty one
// selects `sudo -n` — the non-interactive attempt that fails fast when passwordless sudo is
// not configured, which is how we detect that a password is needed.
func (a *Associate) helperCommand(password string) []string {
	if a.helperPath == "" {
		return nil
	}
	if !a.useSudo {
		return []string{a.helperPath}
	}
	if password != "" {
		return []string{"sudo", "-S", a.helperPath}
	}
	return []string{"sudo", "-n", a.helperPath}
}

// New creates an associate from its enrollment bundle and compiled-in modules.
func New(b bundle.Bundle, mods ...module.Module) *Associate {
	a := &Associate{bundle: b, modules: map[string]module.Module{}}
	for _, m := range mods {
		name := m.Manifest().Name
		a.modules[name] = m
		a.order = append(a.order, name)
	}
	return a
}

// Run dials the manager and serves the stream, reconnecting with backoff until ctx ends.
func (a *Associate) Run(ctx context.Context) error {
	for {
		if err := a.session(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("stream ended; reconnecting", "err", err, "in", reconnectBackoff)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectBackoff):
		}
	}
}

func (a *Associate) session(parent context.Context) error {
	tlsCfg, err := a.clientTLS()
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(a.bundle.ManagerAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
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

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	stream, err := pb.NewManagerServiceClient(conn).Connect(ctx)
	if err != nil {
		return err
	}

	if err := stream.Send(&pb.AssociateMessage{Payload: &pb.AssociateMessage_Hello{Hello: a.hello(ctx)}}); err != nil {
		return err
	}
	ack, err := stream.Recv()
	if err != nil {
		return err
	}
	if h := ack.GetHelloAck(); h == nil || !h.GetAccepted() {
		return errors.New("manager rejected hello")
	}
	slog.Info("connected to manager", "addr", a.bundle.ManagerAddr)

	s := &session{
		a:      a,
		stream: stream,
		ctx:    ctx,
		cancel: cancel,
		outbox: make(chan *pb.AssociateMessage, 64),
		cmds:   make(chan *pb.Command, 16),
		active: map[string]bool{},
		logs:   map[string]context.CancelFunc{},
	}
	go s.sendLoop()
	go s.heartbeatLoop()
	go s.commandWorker()
	s.publishStatuses()

	for {
		msg, err := stream.Recv()
		if err != nil {
			cancel()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		s.handle(msg)
	}
}

// session is one live connection's mutable state.
type session struct {
	a      *Associate
	stream pb.ManagerService_ConnectClient
	ctx    context.Context
	cancel context.CancelFunc
	outbox chan *pb.AssociateMessage
	cmds   chan *pb.Command

	mu     sync.Mutex
	active map[string]bool // job IDs currently queued/running (idempotency)

	logMu sync.Mutex
	logs  map[string]context.CancelFunc // active log streams by stream id
}

func (s *session) sendLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case msg := <-s.outbox:
			if err := s.stream.Send(msg); err != nil {
				s.cancel()
				return
			}
		}
	}
}

func (s *session) send(msg *pb.AssociateMessage) {
	select {
	case s.outbox <- msg:
	case <-s.ctx.Done():
	}
}

func (s *session) heartbeatLoop() {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	s.send(&pb.AssociateMessage{Payload: &pb.AssociateMessage_Heartbeat{Heartbeat: &pb.Heartbeat{SentAt: timestamppb.Now(), Health: gatherHealth()}}})
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.send(&pb.AssociateMessage{Payload: &pb.AssociateMessage_Heartbeat{Heartbeat: &pb.Heartbeat{SentAt: timestamppb.Now(), Health: gatherHealth()}}})
		}
	}
}
