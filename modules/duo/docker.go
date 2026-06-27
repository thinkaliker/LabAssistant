package duo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/thinkaliker/labassistant/module"
)

// dctr is one container as reported by `docker ps --format json`.
type dctr struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Labels string `json:"Labels"` // "k=v,k=v,..."
}

func (c dctr) label(key string) string {
	for _, kv := range strings.Split(c.Labels, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// dockerStatus builds the stack/service view from running + stopped compose containers.
func (m *Module) dockerStatus(ctx context.Context) (module.Status, error) {
	ctrs, err := dockerContainers(ctx)
	if err != nil {
		return module.Status{}, err
	}
	byStack := map[string]*Stack{}
	var order []string
	running, total := 0, 0
	for _, c := range ctrs {
		project := c.label("com.docker.compose.project")
		if project == "" {
			continue // not a compose-managed container
		}
		st, ok := byStack[project]
		if !ok {
			st = &Stack{Name: project, Path: c.label("com.docker.compose.project.config_files")}
			byStack[project] = st
			order = append(order, project)
		}
		status := "stopped"
		if c.State == "running" {
			status = "running"
			running++
		}
		total++
		svc := c.label("com.docker.compose.service")
		if svc == "" {
			svc = c.Names
		}
		st.Services = append(st.Services, &Service{Name: svc, Status: status, Image: c.Image, HasLogs: true})
	}
	stacks := make([]*Stack, 0, len(order))
	for _, name := range order {
		s := byStack[name]
		s.Status = stackStatus(s)
		stacks = append(stacks, s)
	}
	data, _ := json.Marshal(map[string]any{"stacks": stacks})
	return module.Status{Summary: fmt.Sprintf("%d/%d services running", running, total), Data: data}, nil
}

func (m *Module) executeDocker(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	emit(module.Event{Kind: module.EventState, State: module.JobRunning})

	if req.Action == "prune" {
		if err := streamDocker(ctx, emit, "system", "prune", "-f"); err != nil {
			return module.Result{State: module.JobFailed, Error: err.Error()}, nil
		}
		emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
		return module.Result{State: module.JobSucceeded}, nil
	}

	switch req.Action {
	case "start", "stop", "restart":
	default:
		return module.Result{State: module.JobFailed, Error: "unknown action: " + req.Action}, nil
	}
	var p actionParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	if p.Stack == "" {
		return module.Result{State: module.JobFailed, Error: "stack is required"}, nil
	}
	ids, err := m.containerIDs(ctx, p.Stack, p.Service)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	if len(ids) == 0 {
		return module.Result{State: module.JobFailed, Error: "no matching containers"}, nil
	}
	args := append([]string{req.Action}, ids...)
	if err := streamDocker(ctx, emit, args...); err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}

func (m *Module) dockerStreamLogs(ctx context.Context, params json.RawMessage, emit func([]byte)) error {
	var p actionParams
	_ = json.Unmarshal(params, &p)
	ids, err := m.containerIDs(ctx, p.Stack, p.Service)
	if err != nil || len(ids) == 0 {
		return err
	}
	// Follow the first matching container (service-level is the common case).
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", "--tail", "50", ids[0])
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			emit([]byte(sc.Text()))
		}
		close(done)
	}()
	err = cmd.Wait()
	_ = pw.Close()
	<-done
	return err
}

func dockerContainers(ctx context.Context) ([]dctr, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var ctrs []dctr
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var c dctr
		if json.Unmarshal(sc.Bytes(), &c) == nil {
			ctrs = append(ctrs, c)
		}
	}
	return ctrs, sc.Err()
}

func (m *Module) containerIDs(ctx context.Context, stack, service string) ([]string, error) {
	args := []string{"ps", "-a", "--filter", "label=com.docker.compose.project=" + stack, "--format", "{{.ID}}"}
	if service != "" {
		args = append(args, "--filter", "label=com.docker.compose.service="+service)
	}
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ids = append(ids, l)
		}
	}
	return ids, nil
}

func streamDocker(ctx context.Context, emit func(module.Event), args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			emit(module.Event{Kind: module.EventLog, Message: sc.Text()})
		}
		close(done)
	}()
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	return err
}
