// Package duo implements the docker updater/orchestrator module: it reports compose
// stacks and services, controls them (start/stop/restart) at stack or service level, and
// streams container logs.
//
// Slice 3 operates on a simulated in-memory stack set so the Services page and log
// streaming are demonstrable without docker. TODO(real-docker): drive `docker compose`
// (ls/ps/start/stop/restart, logs) when docker is present.
package duo

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/thinkaliker/labassistant/module"
)

// Module is the duo capability.
type Module struct {
	mu     sync.Mutex
	stacks []*Stack
}

// Stack is a compose project and its services.
type Stack struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Status   string     `json:"status"`
	Services []*Service `json:"services"`
}

// Service is a single container/service in a stack.
type Service struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	Image           string `json:"image"`
	UpdateAvailable bool   `json:"updateAvailable"`
	HasLogs         bool   `json:"hasLogs"`
}

// New returns a duo module seeded with a simulated stack set.
func New() *Module {
	return &Module{stacks: []*Stack{
		{Name: "media", Path: "/srv/media/compose.yaml", Status: "running", Services: []*Service{
			{Name: "jellyfin", Status: "running", Image: "jellyfin/jellyfin:10.9", UpdateAvailable: true, HasLogs: true},
			{Name: "sonarr", Status: "running", Image: "linuxserver/sonarr:4.0", HasLogs: true},
		}},
		{Name: "infra", Path: "/srv/infra/compose.yaml", Status: "running", Services: []*Service{
			{Name: "traefik", Status: "running", Image: "traefik:3.1", HasLogs: true},
		}},
	}}
}

func (m *Module) Manifest() module.Manifest {
	params := json.RawMessage(`{"type":"object","properties":{"stack":{"type":"string"},"service":{"type":"string"}},"required":["stack"]}`)
	mk := func(name, desc string) module.ActionSpec {
		return module.ActionSpec{
			Name: name, Description: desc, ParamsSchema: params,
			Privilege: module.PrivilegeElevated, DefaultTimeout: 2 * time.Minute, Streams: true,
		}
	}
	return module.Manifest{
		Name:         "duo",
		Version:      "0.1.0",
		Description:  "Docker updater/orchestrator: manage compose stacks and services.",
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{"registryUser":{"type":"string","title":"Registry user"},"registryToken":{"type":"string","title":"Registry token","secret":true}}}`),
		Actions: []module.ActionSpec{
			mk("start", "Start a stack or service."),
			mk("stop", "Stop a stack or service."),
			mk("restart", "Restart a stack or service."),
			{
				Name:           "prune",
				Description:    "Remove unused images and volumes (destructive).",
				Privilege:      module.PrivilegeElevated,
				Destructive:    true,
				DefaultTimeout: 5 * time.Minute,
				Streams:        true,
			},
		},
	}
}

func (m *Module) Detect(ctx context.Context) (module.Detection, error) {
	caps := map[string]string{"orchestrator": "compose"}
	if _, err := exec.LookPath("docker"); err == nil {
		caps["docker"] = "present"
		caps["mode"] = "simulated" // TODO(real-docker): switch to "docker"
	} else {
		caps["docker"] = "absent"
		caps["mode"] = "simulated"
	}
	return module.Detection{Applicable: true, Capabilities: caps}, nil
}

func (m *Module) Status(ctx context.Context) (module.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, _ := json.Marshal(map[string]any{"stacks": m.stacks})
	running := 0
	total := 0
	for _, s := range m.stacks {
		for _, svc := range s.Services {
			total++
			if svc.Status == "running" {
				running++
			}
		}
	}
	return module.Status{Summary: fmt.Sprintf("%d/%d services running", running, total), Data: data}, nil
}

type actionParams struct {
	Stack   string `json:"stack"`
	Service string `json:"service"`
}

func (m *Module) Execute(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	if req.Action == "prune" {
		return m.prune(ctx, emit)
	}
	var p actionParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return module.Result{State: module.JobFailed, Error: "invalid params: " + err.Error()}, nil
		}
	}
	if p.Stack == "" {
		return module.Result{State: module.JobFailed, Error: "stack is required"}, nil
	}
	target := "stack " + p.Stack
	if p.Service != "" {
		target = fmt.Sprintf("service %s/%s", p.Stack, p.Service)
	}

	var desired string
	switch req.Action {
	case "start":
		desired = "running"
	case "stop":
		desired = "stopped"
	case "restart":
		desired = "running"
	default:
		return module.Result{State: module.JobFailed, Error: "unknown action: " + req.Action}, nil
	}

	emit(module.Event{Kind: module.EventState, State: module.JobRunning})
	emit(module.Event{Kind: module.EventLog, Message: fmt.Sprintf("%sing %s", req.Action, target)})
	select {
	case <-ctx.Done():
		return module.Result{State: module.JobTimedOut, Error: ctx.Err().Error()}, nil
	case <-time.After(600 * time.Millisecond):
	}

	if !m.apply(p, desired) {
		return module.Result{State: module.JobFailed, Error: "no matching stack/service"}, nil
	}
	emit(module.Event{Kind: module.EventLog, Message: target + " is now " + desired})
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}

func (m *Module) prune(ctx context.Context, emit func(module.Event)) (module.Result, error) {
	emit(module.Event{Kind: module.EventState, State: module.JobRunning})
	for _, step := range []string{"deleted 3 dangling images", "reclaimed 412MB"} {
		select {
		case <-ctx.Done():
			return module.Result{State: module.JobTimedOut, Error: ctx.Err().Error()}, nil
		case <-time.After(400 * time.Millisecond):
		}
		emit(module.Event{Kind: module.EventLog, Message: step})
	}
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}

// apply mutates the simulated state; returns false if nothing matched.
func (m *Module) apply(p actionParams, status string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	matched := false
	for _, s := range m.stacks {
		if s.Name != p.Stack {
			continue
		}
		for _, svc := range s.Services {
			if p.Service == "" || svc.Name == p.Service {
				svc.Status = status
				matched = true
			}
		}
		s.Status = stackStatus(s)
	}
	return matched
}

func stackStatus(s *Stack) string {
	running := 0
	for _, svc := range s.Services {
		if svc.Status == "running" {
			running++
		}
	}
	switch {
	case running == 0:
		return "stopped"
	case running == len(s.Services):
		return "running"
	default:
		return "partial"
	}
}

// StreamLogs emits simulated container log lines until ctx is cancelled.
func (m *Module) StreamLogs(ctx context.Context, params json.RawMessage, emit func([]byte)) error {
	var p actionParams
	_ = json.Unmarshal(params, &p)
	src := p.Stack
	if p.Service != "" {
		src = p.Stack + "/" + p.Service
	}
	t := time.NewTicker(700 * time.Millisecond)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			n++
			emit([]byte(fmt.Sprintf("%s [%s] log line %d", time.Now().Format(time.RFC3339), src, n)))
		}
	}
}
