package associate

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/thinkaliker/labassistant/module"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

const associateVersion = "0.1.0"

// hello builds the initial frame advertising every module's manifest + detection.
func (a *Associate) hello(ctx context.Context) *pb.Hello {
	var mods []*pb.ModuleInfo
	for _, name := range a.order {
		m := a.modules[name]
		det, err := m.Detect(ctx)
		if err != nil {
			slog.Warn("module detect failed", "module", name, "err", err)
		}
		mods = append(mods, moduleInfo(m.Manifest(), det))
	}
	return &pb.Hello{
		ProtocolVersion:  ProtocolVersion,
		HostId:           a.bundle.HostID,
		AssociateVersion: associateVersion,
		Modules:          mods,
	}
}

// publishStatuses sends an initial status for each module so the manager has data
// immediately, without waiting for a StatusRequest.
func (s *session) publishStatuses() {
	for _, name := range s.a.order {
		st, err := s.a.modules[name].Status(s.ctx)
		if err != nil {
			slog.Warn("module status failed", "module", name, "err", err)
			continue
		}
		s.send(statusUpdate(name, st.Data))
	}
}

func (s *session) handle(msg *pb.ManagerMessage) {
	switch p := msg.Payload.(type) {
	case *pb.ManagerMessage_Command:
		s.onCommand(p.Command)
	case *pb.ManagerMessage_StatusRequest:
		go s.onStatusRequest(p.StatusRequest)
	case *pb.ManagerMessage_Cancel:
		// TODO(slice-4): cancel a running job.
	case *pb.ManagerMessage_LogRequest:
		// TODO(slice-3): module log streaming.
	}
}

func (s *session) onCommand(cmd *pb.Command) {
	s.mu.Lock()
	if s.active[cmd.GetJobId()] {
		s.mu.Unlock()
		s.send(ack(cmd.GetJobId(), false, "duplicate job"))
		return
	}
	s.active[cmd.GetJobId()] = true
	s.mu.Unlock()

	s.send(ack(cmd.GetJobId(), true, ""))
	select {
	case s.cmds <- cmd:
	case <-s.ctx.Done():
	}
}

func (s *session) commandWorker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case cmd := <-s.cmds:
			s.runCommand(cmd)
		}
	}
}

func (s *session) runCommand(cmd *pb.Command) {
	defer func() {
		s.mu.Lock()
		delete(s.active, cmd.GetJobId())
		s.mu.Unlock()
	}()

	m, ok := s.a.modules[cmd.GetModule()]
	if !ok {
		s.send(jobResult(cmd.GetJobId(), module.JobFailed, nil, "unknown module: "+cmd.GetModule()))
		return
	}

	ctx := s.ctx
	if d := cmd.GetTimeout().AsDuration(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(s.ctx, d)
		defer cancel()
	}

	emit := func(ev module.Event) { s.send(jobEvent(cmd.GetJobId(), ev)) }
	req := module.ActionRequest{JobID: cmd.GetJobId(), Action: cmd.GetAction(), Params: cmd.GetParams()}
	res, err := m.Execute(ctx, req, emit)
	if err != nil {
		s.send(jobResult(cmd.GetJobId(), module.JobFailed, nil, err.Error()))
		return
	}
	s.send(jobResult(cmd.GetJobId(), res.State, res.Data, res.Error))
}

func (s *session) onStatusRequest(req *pb.StatusRequest) {
	for _, name := range s.a.order {
		if req.GetModule() != "" && req.GetModule() != name {
			continue
		}
		st, err := s.a.modules[name].Status(s.ctx)
		if err != nil {
			continue
		}
		s.send(statusUpdate(name, st.Data))
	}
}

// ---- message builders ----

func statusUpdate(name string, data []byte) *pb.AssociateMessage {
	return &pb.AssociateMessage{Payload: &pb.AssociateMessage_Status{Status: &pb.StatusUpdate{
		Module:     name,
		Data:       data,
		ObservedAt: timestamppb.Now(),
	}}}
}

func ack(jobID string, queued bool, reject string) *pb.AssociateMessage {
	return &pb.AssociateMessage{Payload: &pb.AssociateMessage_Ack{Ack: &pb.CommandAck{
		JobId:        jobID,
		Queued:       queued,
		RejectReason: reject,
	}}}
}

func jobEvent(jobID string, ev module.Event) *pb.AssociateMessage {
	return &pb.AssociateMessage{Payload: &pb.AssociateMessage_JobEvent{JobEvent: &pb.JobEvent{
		JobId:    jobID,
		Kind:     eventKindProto(ev.Kind),
		Message:  ev.Message,
		Progress: ev.Progress,
		State:    jobStateProto(ev.State),
		At:       timestamppb.Now(),
	}}}
}

func jobResult(jobID string, st module.JobState, data []byte, errStr string) *pb.AssociateMessage {
	return &pb.AssociateMessage{Payload: &pb.AssociateMessage_JobResult{JobResult: &pb.JobResult{
		JobId: jobID,
		State: jobStateProto(st),
		Data:  data,
		Error: errStr,
	}}}
}

// ---- module -> proto converters ----

func moduleInfo(man module.Manifest, det module.Detection) *pb.ModuleInfo {
	return &pb.ModuleInfo{
		Name:         man.Name,
		Version:      man.Version,
		Description:  man.Description,
		Actions:      actionSpecs(man.Actions),
		Detection:    &pb.Detection{Applicable: det.Applicable, Capabilities: det.Capabilities},
		ConfigSchema: man.ConfigSchema,
	}
}

func actionSpecs(in []module.ActionSpec) []*pb.ActionSpec {
	out := make([]*pb.ActionSpec, 0, len(in))
	for _, a := range in {
		out = append(out, &pb.ActionSpec{
			Name:           a.Name,
			Description:    a.Description,
			ParamsSchema:   a.ParamsSchema,
			ResultSchema:   a.ResultSchema,
			Privilege:      privilegeProto(a.Privilege),
			Destructive:    a.Destructive,
			DefaultTimeout: durationpb.New(a.DefaultTimeout),
			Streams:        a.Streams,
		})
	}
	return out
}

func privilegeProto(p module.Privilege) pb.Privilege {
	if p == module.PrivilegeElevated {
		return pb.Privilege_PRIVILEGE_ELEVATED
	}
	return pb.Privilege_PRIVILEGE_NONE
}

func eventKindProto(k module.EventKind) pb.EventKind {
	switch k {
	case module.EventProgress:
		return pb.EventKind_EVENT_KIND_PROGRESS
	case module.EventState:
		return pb.EventKind_EVENT_KIND_STATE
	default:
		return pb.EventKind_EVENT_KIND_LOG
	}
}

func jobStateProto(s module.JobState) pb.JobState {
	switch s {
	case module.JobRunning:
		return pb.JobState_JOB_STATE_RUNNING
	case module.JobSucceeded:
		return pb.JobState_JOB_STATE_SUCCEEDED
	case module.JobFailed:
		return pb.JobState_JOB_STATE_FAILED
	case module.JobTimedOut:
		return pb.JobState_JOB_STATE_TIMED_OUT
	default:
		return pb.JobState_JOB_STATE_QUEUED
	}
}
