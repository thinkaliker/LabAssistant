package duo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/thinkaliker/labassistant/module"
)

// maxComposeBytes caps the compose file content returned by read-compose.
const maxComposeBytes = 1 << 20 // 1 MiB

// dctr is one container as reported by `docker ps --format json`.
type dctr struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"` // human string, e.g. "Up 2 hours (healthy)"
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
	m.mu.Lock()
	updates := make(map[string]imageUpdate, len(m.updates))
	for k, v := range m.updates {
		updates[k] = v
	}
	m.mu.Unlock()

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
		iu := updates[c.Image]
		st.Services = append(st.Services, &Service{
			Name: svc, Status: status, Health: parseHealth(c.Status), Image: c.Image,
			UpdateAvailable: iu.hasUpdate(),
			CurrentDigest:   iu.Current, LatestDigest: iu.Latest,
			HasLogs: true,
		})
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

// parseHealth extracts a container's healthcheck state from the `docker ps` status string
// (e.g. "Up 2 hours (healthy)"). Empty means the container defines no healthcheck.
func parseHealth(psStatus string) string {
	switch {
	case strings.Contains(psStatus, "(healthy)"):
		return "healthy"
	case strings.Contains(psStatus, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(psStatus, "health: starting"):
		return "starting"
	default:
		return ""
	}
}

func (m *Module) executeDocker(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	var p actionParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}

	// These manage their own event/result lifecycle (read returns data, the rest stream).
	switch req.Action {
	case "read-compose":
		return m.readCompose(ctx, p)
	case "write-compose":
		return m.writeCompose(ctx, p, emit)
	case "check-updates":
		return m.checkUpdates(ctx, p, emit)
	}

	emit(module.Event{Kind: module.EventState, State: module.JobRunning})

	switch req.Action {
	case "prune":
		if err := streamDocker(ctx, emit, "system", "prune", "-f"); err != nil {
			return module.Result{State: module.JobFailed, Error: err.Error()}, nil
		}
		emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
		return module.Result{State: module.JobSucceeded}, nil
	case "deploy":
		return m.deploy(ctx, p, emit)
	case "update":
		return m.update(ctx, p, emit)
	case "start", "stop", "restart":
		// handled below
	default:
		return module.Result{State: module.JobFailed, Error: "unknown action: " + req.Action}, nil
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

// composePath resolves a stack's compose file path from the compose project labels. multiFile
// is true when the project was created from several compose files (comma-separated), in which
// case callers must not blindly overwrite. Querying the label directly (rather than the JSON
// label blob) keeps comma-containing values intact.
func (m *Module) composePath(ctx context.Context, stack string) (path string, multiFile bool, err error) {
	if stack == "" {
		return "", false, fmt.Errorf("stack is required")
	}
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=com.docker.compose.project="+stack,
		"--format", `{{.Label "com.docker.compose.project.config_files"}}`).Output()
	if err != nil {
		return "", false, fmt.Errorf("docker ps: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		return strings.TrimSpace(parts[0]), len(parts) > 1, nil
	}
	return "", false, fmt.Errorf("no compose file recorded for stack %q", stack)
}

// readCompose returns a stack's compose file content in Result.Data.
func (m *Module) readCompose(ctx context.Context, p actionParams) (module.Result, error) {
	path, multi, err := m.composePath(ctx, p.Stack)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: fmt.Sprintf("read %s: %v", path, err)}, nil
	}
	truncated := false
	if len(b) > maxComposeBytes {
		b, truncated = b[:maxComposeBytes], true
	}
	data, _ := json.Marshal(map[string]any{
		"stack": p.Stack, "path": path, "content": string(b),
		"truncated": truncated, "multiFile": multi,
	})
	return module.Result{State: module.JobSucceeded, Data: data}, nil
}

// writeCompose overwrites a stack's compose file, keeping a .bak and validating the result with
// `docker compose config`. On a validation failure the backup is restored and the action fails.
func (m *Module) writeCompose(ctx context.Context, p actionParams, emit func(module.Event)) (module.Result, error) {
	emit(module.Event{Kind: module.EventState, State: module.JobRunning})
	path, multi, err := m.composePath(ctx, p.Stack)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	if multi {
		return module.Result{State: module.JobFailed, Error: "stack uses multiple compose files; refusing to overwrite"}, nil
	}
	mode := os.FileMode(0o644)
	if fi, serr := os.Stat(path); serr == nil {
		mode = fi.Mode().Perm()
	}
	if cur, rerr := os.ReadFile(path); rerr == nil {
		if werr := os.WriteFile(path+".bak", cur, mode); werr != nil {
			return module.Result{State: module.JobFailed, Error: fmt.Sprintf("write backup: %v", werr)}, nil
		}
		emit(module.Event{Kind: module.EventLog, Message: "backed up to " + path + ".bak"})
	}
	if err := os.WriteFile(path, []byte(p.Content), mode); err != nil {
		return module.Result{State: module.JobFailed, Error: fmt.Sprintf("write %s: %v", path, err)}, nil
	}
	emit(module.Event{Kind: module.EventLog, Message: "wrote " + path})

	if out, verr := exec.CommandContext(ctx, "docker", "compose", "-f", path, "config", "-q").CombinedOutput(); verr != nil {
		msg := strings.TrimSpace(string(out))
		emit(module.Event{Kind: module.EventLog, Message: "validation failed: " + msg})
		if cur, rerr := os.ReadFile(path + ".bak"); rerr == nil {
			_ = os.WriteFile(path, cur, mode)
			emit(module.Event{Kind: module.EventLog, Message: "restored from backup"})
		}
		return module.Result{State: module.JobFailed, Error: "compose validation failed: " + msg}, nil
	}
	emit(module.Event{Kind: module.EventLog, Message: "compose file is valid"})
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}

// deploy applies a stack's compose file with `docker compose up -d`.
func (m *Module) deploy(ctx context.Context, p actionParams, emit func(module.Event)) (module.Result, error) {
	path, _, err := m.composePath(ctx, p.Stack)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	args := []string{"compose", "-f", path, "up", "-d"}
	if p.Service != "" {
		args = append(args, p.Service)
	}
	if err := streamDocker(ctx, emit, args...); err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}

// update pulls newer images and recreates containers for a stack or one service.
func (m *Module) update(ctx context.Context, p actionParams, emit func(module.Event)) (module.Result, error) {
	path, _, err := m.composePath(ctx, p.Stack)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	pull := []string{"compose", "-f", path, "pull"}
	up := []string{"compose", "-f", path, "up", "-d"}
	if p.Service != "" {
		pull = append(pull, p.Service)
		up = append(up, p.Service)
	}
	if err := streamDocker(ctx, emit, pull...); err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	if err := streamDocker(ctx, emit, up...); err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	cleared := m.clearUpdates(ctx, p.Stack, p.Service)
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	data, _ := json.Marshal(map[string]any{"cleared": cleared})
	return module.Result{State: module.JobSucceeded, Data: data}, nil
}

// checkUpdates compares each compose service image against its registry and records which have
// a newer image available. It never installs anything. Images it cannot compare are left
// unflagged (reported as "unknown") so there are no false positives.
func (m *Module) checkUpdates(ctx context.Context, p actionParams, emit func(module.Event)) (module.Result, error) {
	emit(module.Event{Kind: module.EventState, State: module.JobRunning})
	ctrs, err := dockerContainers(ctx)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	seen := map[string]bool{}
	var images []string
	for _, c := range ctrs {
		if c.label("com.docker.compose.project") == "" {
			continue
		}
		if p.Stack != "" && c.label("com.docker.compose.project") != p.Stack {
			continue
		}
		if c.Image == "" || seen[c.Image] {
			continue
		}
		seen[c.Image] = true
		images = append(images, c.Image)
	}

	results := map[string]imageUpdate{}
	updated := 0
	for _, img := range images {
		local := localRepoDigest(ctx, img)
		remote := remoteDigest(ctx, img)
		if local == "" || remote == "" {
			emit(module.Event{Kind: module.EventLog, Message: img + ": unknown (could not compare)"})
			continue
		}
		iu := imageUpdate{Current: local, Latest: remote}
		results[img] = iu
		if iu.hasUpdate() {
			updated++
			emit(module.Event{Kind: module.EventLog, Message: fmt.Sprintf("%s: update available (%s -> %s)", img, shortDigest(local), shortDigest(remote))})
		} else {
			emit(module.Event{Kind: module.EventLog, Message: img + ": up to date"})
		}
	}

	m.mu.Lock()
	for img, iu := range results {
		m.updates[img] = iu
	}
	m.mu.Unlock()

	emit(module.Event{Kind: module.EventLog, Message: fmt.Sprintf("%d image(s) with updates", updated)})
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	data, _ := json.Marshal(map[string]any{"images": results, "checked": len(images), "updates": updated})
	return module.Result{State: module.JobSucceeded, Data: data}, nil
}

// clearUpdates drops the update flag for a stack's (or one service's) images after an update
// and returns the images it cleared (so the associate's instance can clear them too).
func (m *Module) clearUpdates(ctx context.Context, stack, service string) []string {
	ctrs, err := dockerContainers(ctx)
	if err != nil {
		return nil
	}
	var cleared []string
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range ctrs {
		if c.label("com.docker.compose.project") != stack {
			continue
		}
		if service != "" && c.label("com.docker.compose.service") != service {
			continue
		}
		delete(m.updates, c.Image)
		cleared = append(cleared, c.Image)
	}
	return cleared
}

// shortDigest abbreviates a "sha256:<hex>" digest to a readable prefix for logs.
func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// localRepoDigest returns the sha256 digest the local image was pulled at, or "".
func localRepoDigest(ctx context.Context, img string) string {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", img,
		"--format", "{{range .RepoDigests}}{{.}}\n{{end}}").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if _, digest, ok := strings.Cut(strings.TrimSpace(l), "@"); ok && isSHA256Digest(digest) {
			return digest
		}
	}
	return ""
}

// remoteDigest returns the sha256 digest the registry currently serves for img's tag, or "".
// It uses `docker buildx imagetools inspect`, which yields the top-level (index) digest that
// matches what `docker image inspect` records in RepoDigests.
func remoteDigest(ctx context.Context, img string) string {
	out, err := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", img,
		"--format", "{{.Manifest.Digest}}").Output()
	if err != nil {
		return ""
	}
	return parseRemoteDigest(string(out))
}

// parseRemoteDigest pulls a clean sha256 digest out of imagetools output. The --format path
// normally prints the bare digest, but some docker/buildx versions ignore the template and
// emit the default human-readable block instead ("Name: ...\nDigest: sha256:...\n"). Scan for
// the first valid digest and reject anything else, so a stray line like "Name: ghcr.io/..."
// never becomes a bogus "latest" that flags a phantom (uninstallable) update.
func parseRemoteDigest(out string) string {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if _, after, ok := strings.Cut(l, "Digest:"); ok { // default-output line
			l = strings.TrimSpace(after)
		}
		if isSHA256Digest(l) {
			return l
		}
	}
	return ""
}

// isSHA256Digest reports whether s is a well-formed "sha256:<64 hex>" digest.
func isSHA256Digest(s string) bool {
	const p = "sha256:"
	if !strings.HasPrefix(s, p) {
		return false
	}
	h := s[len(p):]
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
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
