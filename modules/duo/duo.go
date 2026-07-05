// Package duo implements the docker updater/orchestrator module: it reports compose stacks
// and services, controls them (start/stop/restart/prune) at stack or service level, and
// streams container logs.
//
// When the docker CLI is present it drives real containers (see docker.go); otherwise it
// runs against a simulated in-memory stack set so the Services page and log streaming are
// demonstrable without docker.
package duo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/thinkaliker/labassistant/module"
)

// Module is the duo capability.
type Module struct {
	useDocker bool

	mu      sync.Mutex
	stacks  []*Stack               // simulated state (used only when docker is absent)
	updates map[string]imageUpdate // image -> digests, populated by check-updates
}

// imageUpdate records the digest a local image was pulled at and the digest the registry now
// serves for the same tag. An update is available when both are known and differ.
type imageUpdate struct {
	Current string `json:"current"` // local RepoDigest
	Latest  string `json:"latest"`  // registry digest
}

func (u imageUpdate) hasUpdate() bool {
	return u.Current != "" && u.Latest != "" && u.Current != u.Latest
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
	Health          string `json:"health,omitempty"` // healthy|unhealthy|starting; empty = no healthcheck
	Image           string `json:"image"`
	UpdateAvailable bool   `json:"updateAvailable"`
	CurrentDigest   string `json:"currentDigest,omitempty"`
	LatestDigest    string `json:"latestDigest,omitempty"`
	HasLogs         bool   `json:"hasLogs"`
}

// New returns a duo module, using real docker when the CLI is available.
func New() *Module {
	m := &Module{updates: map[string]imageUpdate{}}
	if _, err := exec.LookPath("docker"); err == nil {
		m.useDocker = true
		return m
	}
	// Without docker the module runs in simulated mode, but only seeds demo stacks
	// when explicitly asked (LABASSISTANT_DEMO=1) so real docker-less hosts stay empty.
	if os.Getenv("LABASSISTANT_DEMO") != "1" {
		return m
	}
	m.stacks = []*Stack{
		{Name: "media", Path: "/srv/media/compose.yaml", Status: "running", Services: []*Service{
			{Name: "jellyfin", Status: "running", Health: "healthy", Image: "jellyfin/jellyfin:10.9", UpdateAvailable: true, HasLogs: true},
			{Name: "sonarr", Status: "running", Health: "starting", Image: "linuxserver/sonarr:4.0", HasLogs: true},
		}},
		{Name: "infra", Path: "/srv/infra/compose.yaml", Status: "running", Services: []*Service{
			{Name: "traefik", Status: "running", Health: "healthy", Image: "traefik:3.1", HasLogs: true},
		}},
	}
	return m
}

func (m *Module) Manifest() module.Manifest {
	params := json.RawMessage(`{"type":"object","properties":{"stack":{"type":"string"},"service":{"type":"string"}},"required":["stack"]}`)
	optStack := json.RawMessage(`{"type":"object","properties":{"stack":{"type":"string"}}}`)
	composeParams := json.RawMessage(`{"type":"object","properties":{"stack":{"type":"string"},"content":{"type":"string"}},"required":["stack","content"]}`)
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
			{
				Name:           "read-compose",
				Description:    "Read a stack's compose file.",
				ParamsSchema:   params,
				Privilege:      module.PrivilegeElevated,
				ReadOnly:       true,
				DefaultTimeout: 30 * time.Second,
			},
			{
				Name:           "write-compose",
				Description:    "Overwrite a stack's compose file (keeps a .bak and validates).",
				ParamsSchema:   composeParams,
				Privilege:      module.PrivilegeElevated,
				DefaultTimeout: 2 * time.Minute,
				Streams:        true,
			},
			{
				Name:           "deploy",
				Description:    "Apply a stack's compose file (docker compose up -d).",
				ParamsSchema:   params,
				Privilege:      module.PrivilegeElevated,
				Destructive:    true,
				DefaultTimeout: 5 * time.Minute,
				Streams:        true,
			},
			{
				Name:           "check-updates",
				Description:    "Check for newer container images without pulling.",
				ParamsSchema:   optStack,
				Privilege:      module.PrivilegeElevated,
				ReadOnly:       true,
				DefaultTimeout: 5 * time.Minute,
				Streams:        true,
			},
			{
				Name:           "update",
				Description:    "Pull newer images and recreate (destructive).",
				ParamsSchema:   params,
				Privilege:      module.PrivilegeElevated,
				Destructive:    true,
				DefaultTimeout: 10 * time.Minute,
				Streams:        true,
			},
		},
	}
}

func (m *Module) Detect(ctx context.Context) (module.Detection, error) {
	mode := "simulated"
	if m.useDocker {
		mode = "docker"
	}
	return module.Detection{Applicable: true, Capabilities: map[string]string{
		"orchestrator": "compose",
		"mode":         mode,
	}}, nil
}

func (m *Module) Status(ctx context.Context) (module.Status, error) {
	if m.useDocker {
		return m.dockerStatus(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, _ := json.Marshal(map[string]any{"stacks": m.stacks})
	running, total := 0, 0
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
	Content string `json:"content"`
}

func (m *Module) Execute(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	if m.useDocker {
		return m.executeDocker(ctx, req, emit)
	}
	return m.executeSimulated(ctx, req, emit)
}

func (m *Module) executeSimulated(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	if req.Action == "prune" {
		return simulate(ctx, emit, "pruning unused images/volumes", "reclaimed space (simulated)")
	}
	var p actionParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return module.Result{State: module.JobFailed, Error: "invalid params: " + err.Error()}, nil
		}
	}

	switch req.Action {
	case "read-compose":
		if p.Stack == "" {
			return module.Result{State: module.JobFailed, Error: "stack is required"}, nil
		}
		content := fmt.Sprintf("# simulated compose for %s\nservices:\n  app:\n    image: example/%s:latest\n", p.Stack, p.Stack)
		data, _ := json.Marshal(map[string]any{
			"stack": p.Stack, "path": "/srv/" + p.Stack + "/compose.yaml",
			"content": content, "truncated": false, "multiFile": false,
		})
		return module.Result{State: module.JobSucceeded, Data: data}, nil
	case "write-compose":
		if p.Stack == "" {
			return module.Result{State: module.JobFailed, Error: "stack is required"}, nil
		}
		return simulate(ctx, emit, "writing compose for "+p.Stack, "wrote compose (simulated)")
	case "check-updates":
		emit(module.Event{Kind: module.EventState, State: module.JobRunning})
		emit(module.Event{Kind: module.EventLog, Message: "checked for image updates (simulated)"})
		emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
		return module.Result{State: module.JobSucceeded}, nil
	case "update":
		if p.Stack == "" {
			return module.Result{State: module.JobFailed, Error: "stack is required"}, nil
		}
		m.clearSimUpdate(p)
		return simulate(ctx, emit, "pulling and recreating "+simTarget(p), "updated (simulated)")
	}

	// start / stop / restart / deploy operate on the simulated stack state.
	if p.Stack == "" {
		return module.Result{State: module.JobFailed, Error: "stack is required"}, nil
	}
	target := simTarget(p)
	var desired string
	switch req.Action {
	case "start", "restart", "deploy":
		desired = "running"
	case "stop":
		desired = "stopped"
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

// IngestResult applies the result of an action that ran in the privileged helper to this
// module instance so Status reflects it. check-updates carries the per-image update map;
// update carries the images it cleared.
func (m *Module) IngestResult(action string, data json.RawMessage) {
	if len(data) == 0 {
		return
	}
	switch action {
	case "check-updates":
		var d struct {
			Images map[string]imageUpdate `json:"images"`
		}
		if json.Unmarshal(data, &d) != nil {
			return
		}
		m.mu.Lock()
		for img, iu := range d.Images {
			m.updates[img] = iu
		}
		m.mu.Unlock()
	case "update":
		var d struct {
			Cleared []string `json:"cleared"`
		}
		if json.Unmarshal(data, &d) != nil {
			return
		}
		m.mu.Lock()
		for _, img := range d.Cleared {
			delete(m.updates, img)
		}
		m.mu.Unlock()
	}
}

// simTarget describes a stack or stack/service for log messages.
func simTarget(p actionParams) string {
	if p.Service != "" {
		return fmt.Sprintf("service %s/%s", p.Stack, p.Service)
	}
	return "stack " + p.Stack
}

// clearSimUpdate flips updateAvailable off on the simulated target after an update.
func (m *Module) clearSimUpdate(p actionParams) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.stacks {
		if s.Name != p.Stack {
			continue
		}
		for _, svc := range s.Services {
			if p.Service == "" || svc.Name == p.Service {
				svc.UpdateAvailable = false
			}
		}
	}
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

// StreamLogs streams container logs (real docker) or simulated lines.
func (m *Module) StreamLogs(ctx context.Context, params json.RawMessage, emit func([]byte)) error {
	if m.useDocker {
		return m.dockerStreamLogs(ctx, params, emit)
	}
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

func simulate(ctx context.Context, emit func(module.Event), startMsg, doneMsg string) (module.Result, error) {
	emit(module.Event{Kind: module.EventState, State: module.JobRunning})
	emit(module.Event{Kind: module.EventLog, Message: startMsg})
	select {
	case <-ctx.Done():
		return module.Result{State: module.JobTimedOut, Error: ctx.Err().Error()}, nil
	case <-time.After(500 * time.Millisecond):
	}
	emit(module.Event{Kind: module.EventLog, Message: doneMsg})
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}
