// Package jobs tracks actions dispatched to associates: their lifecycle state, streamed
// progress/log events, and final result. It feeds both the per-job SSE stream and the
// aggregate event feed.
package jobs

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/thinkaliker/labassistant/manager/events"
	"github.com/thinkaliker/labassistant/module"
)

// Event is a single progress/log/state update for a job.
type Event struct {
	JobID    string    `json:"jobId"`
	Kind     string    `json:"kind"` // log | progress | state
	Message  string    `json:"message,omitempty"`
	Progress float64   `json:"progress,omitempty"`
	State    string    `json:"state,omitempty"`
	At       time.Time `json:"at"`
}

// Job is a dispatched action and its accumulated state.
type Job struct {
	ID        string          `json:"id"`
	HostID    string          `json:"hostId"`
	Module    string          `json:"module"`
	Action    string          `json:"action"`
	Params    json.RawMessage `json:"params,omitempty"`
	State     string          `json:"state"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`

	mu     sync.Mutex
	log    []Event
	broker *events.Broker
}

// View is a mutex-free snapshot of a job's public fields.
type View struct {
	ID        string          `json:"id"`
	HostID    string          `json:"hostId"`
	Module    string          `json:"module"`
	Action    string          `json:"action"`
	Params    json.RawMessage `json:"params,omitempty"`
	State     string          `json:"state"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// Snapshot returns a copy of the job's public fields.
func (j *Job) Snapshot() View {
	j.mu.Lock()
	defer j.mu.Unlock()
	return View{
		ID: j.ID, HostID: j.HostID, Module: j.Module, Action: j.Action,
		Params: j.Params, State: j.State, Result: j.Result, Error: j.Error,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
}

// Subscribe returns buffered events plus a live channel and cancel for new events.
func (j *Job) Subscribe() (backlog []Event, ch <-chan []byte, cancel func()) {
	j.mu.Lock()
	backlog = append(backlog, j.log...)
	j.mu.Unlock()
	c, cancel := j.broker.Subscribe()
	return backlog, c, cancel
}

// Registry holds all jobs and publishes updates to the aggregate feed.
type Registry struct {
	mu         sync.Mutex
	m          map[string]*Job
	global     *events.Broker
	resultHook func(View)
}

// NewRegistry creates an empty registry that mirrors job updates to global.
func NewRegistry(global *events.Broker) *Registry {
	return &Registry{m: map[string]*Job{}, global: global, resultHook: func(View) {}}
}

// SetResultHook registers a callback invoked with a job's snapshot when it reaches a
// terminal state.
func (r *Registry) SetResultHook(fn func(View)) {
	r.mu.Lock()
	r.resultHook = fn
	r.mu.Unlock()
}

// Create registers a new queued job.
func (r *Registry) Create(hostID, mod, action string, params json.RawMessage) *Job {
	j := &Job{
		ID:        uuid.NewString(),
		HostID:    hostID,
		Module:    mod,
		Action:    action,
		Params:    params,
		State:     module.JobQueued.String(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		broker:    events.New(),
	}
	r.mu.Lock()
	r.m[j.ID] = j
	r.mu.Unlock()
	return j
}

// Get returns a job by id.
func (r *Registry) Get(id string) (*Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.m[id]
	return j, ok
}

// List returns snapshots of all jobs.
func (r *Registry) List() []View {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]View, 0, len(r.m))
	for _, j := range r.m {
		out = append(out, j.Snapshot())
	}
	return out
}

// AddEvent appends a progress/log event and publishes it to the job and aggregate feeds.
func (r *Registry) AddEvent(jobID string, ev Event) {
	j, ok := r.Get(jobID)
	if !ok {
		return
	}
	ev.JobID = jobID
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	j.mu.Lock()
	j.log = append(j.log, ev)
	if ev.Kind == "state" && ev.State != "" {
		j.State = ev.State
	}
	j.UpdatedAt = ev.At
	j.mu.Unlock()

	data := encode("job_event", ev)
	j.broker.Publish(data)
	r.global.Publish(data)
}

// SetResult records the terminal state + result and publishes a final state event.
func (r *Registry) SetResult(jobID string, state module.JobState, result json.RawMessage, errStr string) {
	j, ok := r.Get(jobID)
	if !ok {
		return
	}
	j.mu.Lock()
	j.State = state.String()
	j.Result = result
	j.Error = errStr
	j.UpdatedAt = time.Now()
	j.mu.Unlock()
	r.AddEvent(jobID, Event{Kind: "state", State: state.String(), Message: errStr})

	r.mu.Lock()
	hook := r.resultHook
	r.mu.Unlock()
	if hook != nil {
		hook(j.Snapshot())
	}
}

func encode(typ string, payload any) []byte {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{typ, payload})
	return b
}
