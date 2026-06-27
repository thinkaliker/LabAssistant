package hub

import (
	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/state"
	"github.com/thinkaliker/labassistant/module"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

func moduleStates(in []*pb.ModuleInfo) []state.ModuleState {
	out := make([]state.ModuleState, 0, len(in))
	for _, m := range in {
		out = append(out, state.ModuleState{
			Name:         m.GetName(),
			Version:      m.GetVersion(),
			Description:  m.GetDescription(),
			Actions:      actionSpecs(m.GetActions()),
			Detection:    detection(m.GetDetection()),
			ConfigSchema: m.GetConfigSchema(),
		})
	}
	return out
}

func actionSpecs(in []*pb.ActionSpec) []module.ActionSpec {
	out := make([]module.ActionSpec, 0, len(in))
	for _, a := range in {
		out = append(out, module.ActionSpec{
			Name:           a.GetName(),
			Description:    a.GetDescription(),
			ParamsSchema:   a.GetParamsSchema(),
			ResultSchema:   a.GetResultSchema(),
			Privilege:      privilege(a.GetPrivilege()),
			Destructive:    a.GetDestructive(),
			DefaultTimeout: a.GetDefaultTimeout().AsDuration(),
			Streams:        a.GetStreams(),
		})
	}
	return out
}

func detection(d *pb.Detection) module.Detection {
	if d == nil {
		return module.Detection{}
	}
	return module.Detection{Applicable: d.GetApplicable(), Capabilities: d.GetCapabilities()}
}

func privilege(p pb.Privilege) module.Privilege {
	if p == pb.Privilege_PRIVILEGE_ELEVATED {
		return module.PrivilegeElevated
	}
	return module.PrivilegeNone
}

func healthFromProto(h *pb.HostHealth) state.Health {
	if h == nil {
		return state.Health{}
	}
	disks := make([]state.Disk, 0, len(h.GetDisks()))
	for _, d := range h.GetDisks() {
		disks = append(disks, state.Disk{
			Mount:      d.GetMount(),
			TotalBytes: d.GetTotalBytes(),
			UsedBytes:  d.GetUsedBytes(),
		})
	}
	return state.Health{
		CPUPercent:    h.GetCpuPercent(),
		MemPercent:    h.GetMemPercent(),
		UptimeSeconds: h.GetUptimeSeconds(),
		Disks:         disks,
	}
}

func jobEventFromProto(e *pb.JobEvent) jobs.Event {
	return jobs.Event{
		Kind:     eventKind(e.GetKind()),
		Message:  e.GetMessage(),
		Progress: e.GetProgress(),
		State:    jobStateFromProto(e.GetState()).String(),
		At:       e.GetAt().AsTime(),
	}
}

func eventKind(k pb.EventKind) string {
	switch k {
	case pb.EventKind_EVENT_KIND_PROGRESS:
		return "progress"
	case pb.EventKind_EVENT_KIND_STATE:
		return "state"
	default:
		return "log"
	}
}

func jobStateFromProto(s pb.JobState) module.JobState {
	switch s {
	case pb.JobState_JOB_STATE_RUNNING:
		return module.JobRunning
	case pb.JobState_JOB_STATE_SUCCEEDED:
		return module.JobSucceeded
	case pb.JobState_JOB_STATE_FAILED:
		return module.JobFailed
	case pb.JobState_JOB_STATE_TIMED_OUT:
		return module.JobTimedOut
	default:
		return module.JobQueued
	}
}
