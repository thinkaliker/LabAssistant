// Package hub implements the manager's gRPC ManagerService: it accepts the associates'
// dial-home mTLS streams, tracks liveness, ingests status/heartbeat/job messages, and
// dispatches commands down each stream.
package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/state"
	"github.com/thinkaliker/labassistant/module"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// ProtocolVersion is the wire protocol version the manager speaks.
const ProtocolVersion = 1

// ErrNotConnected is returned by Dispatch when a host has no live stream.
var ErrNotConnected = errors.New("host not connected")

type conn struct {
	hostID string
	send   chan *pb.ManagerMessage
}

// Hub is the gRPC ManagerService server and the registry of live associate streams.
type Hub struct {
	pb.UnimplementedManagerServiceServer

	store *state.Store
	jobs  *jobs.Registry

	mu    sync.Mutex
	conns map[string]*conn

	logMu sync.Mutex
	logs  map[string]chan []byte // streamID -> log chunk channel
}

// New creates a Hub backed by the host registry and job registry.
func New(store *state.Store, jr *jobs.Registry) *Hub {
	return &Hub{store: store, jobs: jr, conns: map[string]*conn{}, logs: map[string]chan []byte{}}
}

// Connected reports whether a host has a live stream.
func (h *Hub) Connected(hostID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[hostID]
	return ok
}

// Dispatch sends a command down a host's stream.
func (h *Hub) Dispatch(hostID string, cmd *pb.Command) error {
	return h.sendTo(hostID, &pb.ManagerMessage{Payload: &pb.ManagerMessage_Command{Command: cmd}})
}

// RotateCert pushes a newly issued client certificate to a connected host.
func (h *Hub) RotateCert(hostID string, certPEM, keyPEM []byte) error {
	return h.sendTo(hostID, &pb.ManagerMessage{Payload: &pb.ManagerMessage_RotateCert{
		RotateCert: &pb.RotateCert{CertPem: certPEM, KeyPem: keyPEM},
	}})
}

// Uninstall tells a connected associate to remove itself from the host and exit.
func (h *Hub) Uninstall(hostID string) error {
	return h.sendTo(hostID, &pb.ManagerMessage{Payload: &pb.ManagerMessage_Uninstall{
		Uninstall: &pb.Uninstall{Reason: "removed by operator"},
	}})
}

func (h *Hub) sendTo(hostID string, msg *pb.ManagerMessage) error {
	h.mu.Lock()
	c, ok := h.conns[hostID]
	h.mu.Unlock()
	if !ok {
		return ErrNotConnected
	}
	select {
	case c.send <- msg:
		return nil
	default:
		return errors.New("send buffer full")
	}
}

// OpenLogStream asks a host's module to stream logs. It returns a stream id, a channel of
// log lines, and a cancel function the caller must invoke when done (it tells the associate
// to stop and releases the channel).
func (h *Hub) OpenLogStream(hostID, moduleName string, params []byte) (string, <-chan []byte, func(), error) {
	streamID := uuid.NewString()
	ch := make(chan []byte, 128)
	h.logMu.Lock()
	h.logs[streamID] = ch
	h.logMu.Unlock()

	req := &pb.ManagerMessage{Payload: &pb.ManagerMessage_LogRequest{LogRequest: &pb.LogStreamRequest{
		StreamId: streamID, Module: moduleName, Params: params,
	}}}
	if err := h.sendTo(hostID, req); err != nil {
		h.closeLog(streamID)
		return "", nil, nil, err
	}
	cancel := func() {
		_ = h.sendTo(hostID, &pb.ManagerMessage{Payload: &pb.ManagerMessage_LogRequest{
			LogRequest: &pb.LogStreamRequest{StreamId: streamID, Stop: true},
		}})
		h.closeLog(streamID)
	}
	return streamID, ch, cancel, nil
}

func (h *Hub) closeLog(streamID string) {
	h.logMu.Lock()
	if ch, ok := h.logs[streamID]; ok {
		delete(h.logs, streamID)
		close(ch)
	}
	h.logMu.Unlock()
}

// Connect is the bidirectional stream RPC the associate dials.
func (h *Hub) Connect(stream pb.ManagerService_ConnectServer) error {
	ctx := stream.Context()
	hostID, err := hostIDFromCert(ctx)
	if err != nil {
		return err
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return fmt.Errorf("first message must be Hello")
	}
	if hello.GetHostId() != hostID {
		return fmt.Errorf("host id mismatch: cert=%q hello=%q", hostID, hello.GetHostId())
	}
	if _, ok := h.store.Get(hostID); !ok {
		return fmt.Errorf("unknown host %q", hostID)
	}

	c := &conn{hostID: hostID, send: make(chan *pb.ManagerMessage, 16)}
	h.register(c)
	defer h.unregister(c)

	h.store.SetOnline(hostID, moduleStates(hello.GetModules()))
	slog.Info("associate connected", "host", hostID, "modules", len(hello.GetModules()))

	c.send <- &pb.ManagerMessage{Payload: &pb.ManagerMessage_HelloAck{HelloAck: &pb.HelloAck{
		Accepted:        true,
		ProtocolVersion: ProtocolVersion,
		ServerTime:      timestamppb.Now(),
	}}}

	go h.sendLoop(ctx, stream, c)

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		h.handle(hostID, msg)
	}
}

func (h *Hub) sendLoop(ctx context.Context, stream pb.ManagerService_ConnectServer, c *conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.send:
			if err := stream.Send(msg); err != nil {
				return
			}
		}
	}
}

func (h *Hub) handle(hostID string, msg *pb.AssociateMessage) {
	switch p := msg.Payload.(type) {
	case *pb.AssociateMessage_Heartbeat:
		h.store.SetHealth(hostID, healthFromProto(p.Heartbeat.GetHealth()))
	case *pb.AssociateMessage_Status:
		su := p.Status
		h.store.SetModuleStatus(hostID, su.GetModule(), su.GetData(), su.GetObservedAt().AsTime())
	case *pb.AssociateMessage_JobEvent:
		h.jobs.AddEvent(p.JobEvent.GetJobId(), jobEventFromProto(p.JobEvent))
	case *pb.AssociateMessage_JobResult:
		jr := p.JobResult
		h.jobs.SetResult(jr.GetJobId(), jobStateFromProto(jr.GetState()), jr.GetData(), jr.GetError())
	case *pb.AssociateMessage_Ack:
		if r := p.Ack.GetRejectReason(); r != "" {
			h.jobs.SetResult(p.Ack.GetJobId(), module.JobFailed, nil, r)
		}
	case *pb.AssociateMessage_LogChunk:
		lc := p.LogChunk
		h.logMu.Lock()
		ch, ok := h.logs[lc.GetStreamId()]
		h.logMu.Unlock()
		if ok && len(lc.GetData()) > 0 {
			select {
			case ch <- lc.GetData():
			default:
			}
		}
	}
}

func (h *Hub) register(c *conn) {
	h.mu.Lock()
	if old, ok := h.conns[c.hostID]; ok {
		close(old.send)
	}
	h.conns[c.hostID] = c
	h.mu.Unlock()
}

func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	if cur, ok := h.conns[c.hostID]; ok && cur == c {
		delete(h.conns, c.hostID)
	}
	h.mu.Unlock()
	h.store.SetOffline(c.hostID)
	slog.Info("associate disconnected", "host", c.hostID)
}

func hostIDFromCert(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", errors.New("no peer in context")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", errors.New("connection is not mTLS")
	}
	if len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
		return "", errors.New("no verified client certificate")
	}
	cn := ti.State.VerifiedChains[0][0].Subject.CommonName
	if cn == "" {
		return "", errors.New("client certificate has empty CommonName")
	}
	return cn, nil
}
