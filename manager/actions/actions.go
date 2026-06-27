// Package actions centralizes how an action gets run on a host. A destructive action
// requires approval before it is dispatched, unless it is pre-approved (e.g. a scheduled
// task whose creator opted in). Both the REST API and the scheduler go through Runner.
package actions

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/thinkaliker/labassistant/manager/auditor"
	"github.com/thinkaliker/labassistant/manager/events"
	"github.com/thinkaliker/labassistant/manager/hub"
	"github.com/thinkaliker/labassistant/manager/jobs"
	"github.com/thinkaliker/labassistant/manager/state"
	"github.com/thinkaliker/labassistant/module"
	pb "github.com/thinkaliker/labassistant/proto/v1"
)

// Sentinel errors mapped to HTTP statuses by the API layer.
var (
	ErrHostNotFound = errors.New("host not found")
	ErrOffline      = errors.New("host not connected")
	ErrNotFound     = errors.New("not found")
)

// Approval is a destructive action awaiting confirmation.
type Approval struct {
	ID          string          `json:"id"`
	HostID      string          `json:"hostId"`
	Module      string          `json:"module"`
	Action      string          `json:"action"`
	Params      json.RawMessage `json:"params,omitempty"`
	Destructive bool            `json:"destructive"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// Outcome is the result of Run: exactly one of JobID or ApprovalID is set.
type Outcome struct {
	JobID      string `json:"jobId,omitempty"`
	ApprovalID string `json:"approvalId,omitempty"`
}

// Runner dispatches actions and holds pending approvals.
type Runner struct {
	store  *state.Store
	jobs   *jobs.Registry
	hub    *hub.Hub
	events *events.Broker
	aud    *auditor.Auditor

	mu        sync.Mutex
	approvals map[string]*Approval
}

// NewRunner builds a Runner.
func NewRunner(store *state.Store, jr *jobs.Registry, h *hub.Hub, ev *events.Broker, aud *auditor.Auditor) *Runner {
	return &Runner{store: store, jobs: jr, hub: h, events: ev, aud: aud, approvals: map[string]*Approval{}}
}

// Run dispatches an action, or creates an approval if the action is destructive and not
// pre-approved.
func (r *Runner) Run(hostID, moduleName, action string, params json.RawMessage, preApproved bool) (Outcome, error) {
	host, ok := r.store.Get(hostID)
	if !ok {
		return Outcome{}, ErrHostNotFound
	}
	if !r.hub.Connected(hostID) {
		return Outcome{}, ErrOffline
	}
	if actionDestructive(host, moduleName, action) && !preApproved {
		ap := &Approval{
			ID: uuid.NewString(), HostID: hostID, Module: moduleName, Action: action,
			Params: params, Destructive: true, CreatedAt: time.Now(),
		}
		r.mu.Lock()
		r.approvals[ap.ID] = ap
		r.mu.Unlock()
		r.events.Publish(envelope("approval_created", ap))
		r.aud.Record("approval_created", hostID, "user",
			fmt.Sprintf("approval requested: %s %s", moduleName, action), ap)
		return Outcome{ApprovalID: ap.ID}, nil
	}
	return r.dispatch(host, moduleName, action, params)
}

func (r *Runner) dispatch(host state.Host, moduleName, action string, params json.RawMessage) (Outcome, error) {
	job := r.jobs.Create(host.ID, moduleName, action, params)
	cmd := &pb.Command{
		JobId:   job.ID,
		Module:  moduleName,
		Action:  action,
		Params:  params,
		Timeout: durationpb.New(actionTimeout(host, moduleName, action)),
	}
	if err := r.hub.Dispatch(host.ID, cmd); err != nil {
		r.jobs.SetResult(job.ID, module.JobFailed, nil, err.Error())
		return Outcome{}, err
	}
	r.aud.Record("action_dispatched", host.ID, "manager",
		fmt.Sprintf("%s %s dispatched", moduleName, action),
		map[string]string{"jobId": job.ID, "module": moduleName, "action": action})
	return Outcome{JobID: job.ID}, nil
}

// Approvals returns pending approvals.
func (r *Runner) Approvals() []Approval {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Approval, 0, len(r.approvals))
	for _, a := range r.approvals {
		out = append(out, *a)
	}
	return out
}

// Confirm dispatches a pending approval.
func (r *Runner) Confirm(id string) (Outcome, error) {
	ap, ok := r.take(id)
	if !ok {
		return Outcome{}, ErrNotFound
	}
	r.events.Publish(envelope("approval_resolved", map[string]string{"id": id, "result": "confirmed"}))
	r.aud.Record("approval_confirmed", ap.HostID, "user",
		fmt.Sprintf("approved: %s %s", ap.Module, ap.Action), nil)
	host, ok := r.store.Get(ap.HostID)
	if !ok {
		return Outcome{}, ErrHostNotFound
	}
	if !r.hub.Connected(ap.HostID) {
		return Outcome{}, ErrOffline
	}
	return r.dispatch(host, ap.Module, ap.Action, ap.Params)
}

// Reject discards a pending approval.
func (r *Runner) Reject(id string) bool {
	ap, ok := r.take(id)
	if ok {
		r.events.Publish(envelope("approval_resolved", map[string]string{"id": id, "result": "rejected"}))
		r.aud.Record("approval_rejected", ap.HostID, "user",
			fmt.Sprintf("rejected: %s %s", ap.Module, ap.Action), nil)
	}
	return ok
}

func (r *Runner) take(id string) (*Approval, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ap, ok := r.approvals[id]
	if ok {
		delete(r.approvals, id)
	}
	return ap, ok
}

// IsDestructive reports whether an action is destructive per the host's advertised manifest.
func (r *Runner) IsDestructive(hostID, moduleName, action string) bool {
	host, ok := r.store.Get(hostID)
	if !ok {
		return false
	}
	return actionDestructive(host, moduleName, action)
}

func actionDestructive(h state.Host, moduleName, action string) bool {
	for _, m := range h.Modules {
		if m.Name != moduleName {
			continue
		}
		for _, a := range m.Actions {
			if a.Name == action {
				return a.Destructive
			}
		}
	}
	return false
}

func actionTimeout(h state.Host, moduleName, action string) time.Duration {
	for _, m := range h.Modules {
		if m.Name != moduleName {
			continue
		}
		for _, a := range m.Actions {
			if a.Name == action && a.DefaultTimeout > 0 {
				return a.DefaultTimeout
			}
		}
	}
	return 5 * time.Minute
}

func envelope(typ string, payload any) []byte {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{typ, payload})
	return b
}
