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
		Actions: []module.ActionSpec{{
			Name:           "apply",
			Description:    "Apply available package updates.",
			Privilege:      module.PrivilegeElevated,
			Destructive:    false,
			DefaultTimeout: 10 * time.Minute,
			Streams:        true,
		}},
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
	Count    int      `json:"count"`
	Packages []string `json:"packages,omitempty"`
	Mode     string   `json:"mode"`
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
	pkgs := []string{"bash", "openssl", "curl"}
	data, _ := json.Marshal(statusData{Count: len(pkgs), Packages: pkgs, Mode: "simulated"})
	return module.Status{Summary: fmt.Sprintf("%d updates available (simulated)", len(pkgs)), Data: data}, nil
}

func (m *Module) Execute(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	if req.Action != "apply" {
		return module.Result{State: module.JobFailed, Error: "unknown action: " + req.Action}, nil
	}
	emit(module.Event{Kind: module.EventState, State: module.JobRunning})
	if hasApt() {
		return m.applyApt(ctx, req, emit)
	}
	return m.applySimulated(ctx, req, emit)
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

func aptUpgradable(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "apt", "list", "--upgradable").Output()
	if err != nil {
		return nil, fmt.Errorf("apt list: %w", err)
	}
	var pkgs []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "Listing") {
			continue
		}
		if name, _, ok := strings.Cut(line, "/"); ok {
			pkgs = append(pkgs, name)
		}
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
