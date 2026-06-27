// Package module defines the contract that every LabAssistant capability implements.
//
// A module is a Go package implementing [Module]. Everything the manager and dashboard
// need is exposed as data through [Manifest], so adding a capability requires no changes
// to the manager or dashboard. duo, qup, and sys are the first three implementations.
//
// See API.md for the full specification.
package module

import (
	"context"
	"encoding/json"
	"time"
)

// Module is implemented by every capability the associate can run on a host.
type Module interface {
	// Manifest returns static metadata and the list of supported actions.
	Manifest() Manifest

	// Detect reports whether the module applies to this host and what it found
	// (e.g. distro, orchestrator). Drives capability negotiation.
	Detect(ctx context.Context) (Detection, error)

	// Status is a read-only / dry-run snapshot of current state. It never mutates the host.
	Status(ctx context.Context) (Status, error)

	// Execute runs a named action. emit streams progress and log events for long actions.
	Execute(ctx context.Context, req ActionRequest, emit func(Event)) (Result, error)
}

// LogStreamer is an optional interface for modules that can stream logs (e.g. duo for
// container logs, sys for system logs). The associate routes a manager log request to the
// module's StreamLogs; it returns when ctx is cancelled or the source ends. params is a
// module-defined JSON selector (container name, unit, follow, ...).
type LogStreamer interface {
	StreamLogs(ctx context.Context, params json.RawMessage, emit func(line []byte)) error
}

// Manifest is the static, data-only description of a module.
type Manifest struct {
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Description string       `json:"description"`
	Actions     []ActionSpec `json:"actions"`
	// ConfigSchema is an optional JSON Schema for module-level per-host configuration
	// (e.g. a private-registry credential for duo). The manager stores this config per
	// host and keeps any secrets outside state.json.
	ConfigSchema json.RawMessage `json:"configSchema,omitempty"`
}

// ActionSpec describes a single action a module exposes.
type ActionSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// ParamsSchema is a JSON Schema for the action's params; validated before dispatch
	// and used by the dashboard to render a generic form.
	ParamsSchema json.RawMessage `json:"paramsSchema,omitempty"`
	// ResultSchema is a JSON Schema describing Result.Data.
	ResultSchema json.RawMessage `json:"resultSchema,omitempty"`
	// Privilege is declared per action, not per module. Elevated actions run via the
	// associate's single privileged helper.
	Privilege Privilege `json:"privilege"`
	// Destructive actions require confirmation to run manually and explicit opt-in to schedule.
	Destructive bool `json:"destructive"`
	// DefaultTimeout is overridable per task.
	DefaultTimeout time.Duration `json:"defaultTimeout"`
	// Streams is true when the action emits Events during execution.
	Streams bool `json:"streams"`
}

// Detection reports a module's applicability to a host and any detected capabilities.
type Detection struct {
	Applicable   bool              `json:"applicable"`
	Capabilities map[string]string `json:"capabilities,omitempty"` // e.g. {"distro":"debian","orchestrator":"compose"}
}

// Status is a read-only snapshot returned by Module.Status.
type Status struct {
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ActionRequest is a single invocation of an action.
type ActionRequest struct {
	JobID  string          `json:"jobId"`  // manager-issued; idempotency key
	Action string          `json:"action"` // must match an ActionSpec.Name
	Params json.RawMessage `json:"params,omitempty"`
}

// Event is a progress or log update emitted during Execute.
type Event struct {
	JobID    string    `json:"jobId"`
	Kind     EventKind `json:"kind"`
	Message  string    `json:"message,omitempty"`
	Progress float64   `json:"progress,omitempty"` // 0.0–1.0 when Kind == EventProgress
	State    JobState  `json:"state,omitempty"`    // set when Kind == EventState
}

// Result is the terminal outcome of Execute.
type Result struct {
	State JobState        `json:"state"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Privilege is the elevation an action requires.
type Privilege int

const (
	PrivilegeNone Privilege = iota
	PrivilegeElevated
)

func (p Privilege) String() string {
	switch p {
	case PrivilegeElevated:
		return "elevated"
	default:
		return "none"
	}
}

// EventKind classifies an Event.
type EventKind int

const (
	EventLog EventKind = iota
	EventProgress
	EventState
)

func (k EventKind) String() string {
	switch k {
	case EventProgress:
		return "progress"
	case EventState:
		return "state"
	default:
		return "log"
	}
}

// JobState is the lifecycle state of a job.
type JobState int

const (
	JobQueued JobState = iota
	JobRunning
	JobSucceeded
	JobFailed
	JobTimedOut
)

func (s JobState) String() string {
	switch s {
	case JobQueued:
		return "queued"
	case JobRunning:
		return "running"
	case JobSucceeded:
		return "succeeded"
	case JobFailed:
		return "failed"
	case JobTimedOut:
		return "timed_out"
	default:
		return "unknown"
	}
}
