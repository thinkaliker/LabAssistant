// Package qup implements the "quick updater" module: it reports and applies host package
// updates. It targets Debian-based hosts via apt; on hosts without apt it runs in a
// simulated mode so the walking skeleton is demonstrable anywhere.
package qup

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/thinkaliker/labassistant/module"
)

// Module is the qup capability.
type Module struct{}

// New returns a qup module.
func New() *Module { return &Module{} }

func (m *Module) Manifest() module.Manifest {
	return module.Manifest{
		Name:        "qup",
		Version:     "0.1.0",
		Description: "Quick updater: report and apply host package updates.",
		Actions: []module.ActionSpec{
			{
				Name:           "check-updates",
				Description:    "Refresh the package index and report available updates (does not install).",
				Privilege:      module.PrivilegeElevated,
				Destructive:    false,
				DefaultTimeout: 5 * time.Minute,
				Streams:        true,
			},
			{
				Name:           "apply",
				Description:    "Apply available package updates.",
				Privilege:      module.PrivilegeElevated,
				Destructive:    true,
				DefaultTimeout: 10 * time.Minute,
				Streams:        true,
			},
		},
	}
}

func (m *Module) Detect(ctx context.Context) (module.Detection, error) {
	if hasApt() {
		return module.Detection{
			Applicable: true,
			Capabilities: map[string]string{
				"packageManager": "apt",
				"mode":           "apt",
			},
		}, nil
	}
	return module.Detection{
		Applicable: true,
		Capabilities: map[string]string{
			"packageManager": "none",
			"mode":           "simulated",
			"os":             runtime.GOOS,
		},
	}, nil
}

// statusData is the JSON payload reported by Status (the manager sums "count" for the
// overview's "updates available").
type statusData struct {
	Count    int         `json:"count"`
	Packages []pkgUpdate `json:"packages,omitempty"`
	Mode     string      `json:"mode"`
}

// pkgUpdate is one upgradable package and the version transition it represents.
type pkgUpdate struct {
	Name      string `json:"name"`
	Current   string `json:"current,omitempty"`
	Candidate string `json:"candidate,omitempty"`
}

func (m *Module) Status(ctx context.Context) (module.Status, error) {
	if hasApt() {
		pkgs, err := aptUpgradable(ctx)
		if err != nil {
			return module.Status{}, err
		}
		data, _ := json.Marshal(statusData{Count: len(pkgs), Packages: pkgs, Mode: "apt"})
		return module.Status{Summary: fmt.Sprintf("%d updates available", len(pkgs)), Data: data}, nil
	}
	pkgs := simulatedPkgs()
	data, _ := json.Marshal(statusData{Count: len(pkgs), Packages: pkgs, Mode: "simulated"})
	return module.Status{Summary: fmt.Sprintf("%d updates available (simulated)", len(pkgs)), Data: data}, nil
}

// simulatedPkgs is the canned upgradable list used on hosts without apt.
func simulatedPkgs() []pkgUpdate {
	return []pkgUpdate{
		{Name: "bash", Current: "5.2.15-2+b7", Candidate: "5.2.15-2+b8"},
		{Name: "openssl", Current: "3.0.11-1~deb12u2", Candidate: "3.0.13-1~deb12u1"},
		{Name: "curl", Current: "7.88.1-10+deb12u5", Candidate: "7.88.1-10+deb12u6"},
	}
}

func (m *Module) Execute(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	switch req.Action {
	case "apply":
		emit(module.Event{Kind: module.EventState, State: module.JobRunning})
		if hasApt() {
			return m.applyApt(ctx, req, emit)
		}
		return m.applySimulated(ctx, req, emit)
	case "check-updates":
		emit(module.Event{Kind: module.EventState, State: module.JobRunning})
		if hasApt() {
			return m.checkApt(ctx, req, emit)
		}
		return m.checkSimulated(ctx, req, emit)
	default:
		return module.Result{State: module.JobFailed, Error: "unknown action: " + req.Action}, nil
	}
}

// checkApt refreshes the apt package index then reports what is upgradable, without installing.
func (m *Module) checkApt(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	cmd := exec.CommandContext(ctx, "apt-get", "update")
	cmd.Env = append(cmd.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if err := streamCmd(cmd, req.JobID, emit); err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	pkgs, err := aptUpgradable(ctx)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	for _, p := range pkgs {
		emit(module.Event{Kind: module.EventLog, Message: "upgradable: " + p.line()})
	}
	emit(module.Event{Kind: module.EventLog, Message: fmt.Sprintf("%d package(s) upgradable", len(pkgs))})
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	data, _ := json.Marshal(statusData{Count: len(pkgs), Packages: pkgs, Mode: "apt"})
	return module.Result{State: module.JobSucceeded, Data: data}, nil
}

func (m *Module) checkSimulated(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	pkgs := simulatedPkgs()
	for _, p := range pkgs {
		emit(module.Event{Kind: module.EventLog, Message: "upgradable: " + p.line()})
	}
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	data, _ := json.Marshal(statusData{Count: len(pkgs), Packages: pkgs, Mode: "simulated"})
	return module.Result{State: module.JobSucceeded, Data: data}, nil
}

// line renders a package as "name (current -> candidate)" for log output.
func (p pkgUpdate) line() string {
	if p.Current != "" || p.Candidate != "" {
		return fmt.Sprintf("%s (%s -> %s)", p.Name, p.Current, p.Candidate)
	}
	return p.Name
}

func (m *Module) applyApt(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	cmd := exec.CommandContext(ctx, "apt-get", "-y", "upgrade")
	cmd.Env = append(cmd.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if err := streamCmd(cmd, req.JobID, emit); err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}

func (m *Module) applySimulated(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	steps := []string{"bash", "openssl", "curl"}
	for i, pkg := range steps {
		select {
		case <-ctx.Done():
			return module.Result{State: module.JobTimedOut, Error: ctx.Err().Error()}, nil
		case <-time.After(400 * time.Millisecond):
		}
		emit(module.Event{Kind: module.EventLog, Message: "upgrading " + pkg})
		emit(module.Event{Kind: module.EventProgress, Progress: float64(i+1) / float64(len(steps))})
	}
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}

func hasApt() bool {
	_, err := exec.LookPath("apt-get")
	return err == nil
}

// aptUpgradable parses `apt list --upgradable`, whose lines look like:
//
//	docker-ce/trixie 5:29.6.1-1~deb13 amd64 [upgradable from: 5:29.6.0-1~deb13]
//
// yielding the package name, candidate version (field after the suite), and current version
// (the "upgradable from:" value).
func aptUpgradable(ctx context.Context) ([]pkgUpdate, error) {
	out, err := exec.CommandContext(ctx, "apt", "list", "--upgradable").Output()
	if err != nil {
		return nil, fmt.Errorf("apt list: %w", err)
	}
	var pkgs []pkgUpdate
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "Listing") {
			continue
		}
		name, rest, ok := strings.Cut(line, "/")
		if !ok {
			continue
		}
		p := pkgUpdate{Name: name}
		if fields := strings.Fields(rest); len(fields) >= 2 {
			p.Candidate = fields[1] // [suite, candidate, arch, ...]
		}
		if i := strings.Index(line, "upgradable from: "); i >= 0 {
			p.Current = strings.TrimSuffix(strings.TrimSpace(line[i+len("upgradable from: "):]), "]")
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, sc.Err()
}

func streamCmd(cmd *exec.Cmd, jobID string, emit func(module.Event)) error {
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
