// Package sys implements host-level system actions: reboot, restarting a system service,
// uptime, disk usage, and network-interface listing, plus streaming system logs. Actions
// are an explicit, enumerated set (never arbitrary command execution); privilege is
// declared per action.
//
// reboot and restart-service act on the host for real, run through the privileged helper:
// systemd or OpenRC on Linux, shutdown / launchctl on macOS. An OS without a known command
// fails the action rather than pretending. Setting LABASSISTANT_SYS_SIMULATE=1 on the
// associate swaps them for a harmless simulation (local testing); nothing else does, since
// the host OS says nothing about whether a machine is safe to reboot.
package sys

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/thinkaliker/labassistant/module"
)

// SimulateEnv, set to "1" on the associate, makes reboot/restart-service simulate instead of
// acting on the host. The associate forwards it to the privileged helper (it is listed in
// elevated.ForwardedEnv), because sudo resets the helper's environment.
const SimulateEnv = "LABASSISTANT_SYS_SIMULATE"

// simulated reports whether reboot/restart-service are swapped for a simulation.
func simulated() bool { return os.Getenv(SimulateEnv) == "1" }

// mode is shown in the Detect capabilities on the dashboard's Hosts page: "simulated" when
// opted out, "unsupported" on an OS this module cannot reboot, else "real".
func mode() string {
	if simulated() {
		return "simulated"
	}
	if _, err := rebootArgv(runtime.GOOS, hasSystemctl()); err != nil {
		return "unsupported"
	}
	return "real"
}

// Module is the sys capability.
type Module struct{}

// New returns a sys module.
func New() *Module { return &Module{} }

func (m *Module) Manifest() module.Manifest {
	svcParams := json.RawMessage(`{"type":"object","properties":{"service":{"type":"string"}},"required":["service"]}`)
	return module.Manifest{
		Name:        "sys",
		Version:     "0.1.0",
		Description: "Host system actions: reboot, service restart, uptime, disk usage, interfaces.",
		Actions: []module.ActionSpec{
			{Name: "reboot", Description: "Reboot the host.", Privilege: module.PrivilegeElevated, Destructive: true, DefaultTimeout: time.Minute, Streams: true},
			{Name: "restart-service", Description: "Restart a system service.", ParamsSchema: svcParams, Privilege: module.PrivilegeElevated, DefaultTimeout: time.Minute, Streams: true},
			{Name: "uptime", Description: "Show host uptime.", Privilege: module.PrivilegeNone, DefaultTimeout: 15 * time.Second, ReadOnly: true},
			{Name: "disk-usage", Description: "Show disk usage.", Privilege: module.PrivilegeNone, DefaultTimeout: 15 * time.Second, ReadOnly: true},
			{Name: "list-interfaces", Description: "List network interfaces.", Privilege: module.PrivilegeNone, DefaultTimeout: 15 * time.Second, ReadOnly: true},
		},
	}
}

func (m *Module) Detect(ctx context.Context) (module.Detection, error) {
	return module.Detection{Applicable: true, Capabilities: map[string]string{"os": runtime.GOOS, "mode": mode()}}, nil
}

func (m *Module) Status(ctx context.Context) (module.Status, error) {
	out := strings.TrimSpace(runCmd(ctx, "uptime"))
	data, _ := json.Marshal(map[string]string{"uptime": out})
	return module.Status{Summary: out, Data: data}, nil
}

func (m *Module) Execute(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	switch req.Action {
	case "reboot":
		if simulated() {
			return m.simulate(ctx, emit, "rebooting host (simulated)", "host would reboot now")
		}
		argv, err := rebootArgv(runtime.GOOS, hasSystemctl())
		if err != nil {
			return module.Result{State: module.JobFailed, Error: err.Error()}, nil
		}
		return runReal(ctx, emit, "requesting reboot; the host goes offline and reconnects once it is back up", argv...)
	case "restart-service":
		var p struct {
			Service string `json:"service"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.Service == "" {
			return module.Result{State: module.JobFailed, Error: "service is required"}, nil
		}
		if !validService(p.Service) {
			return module.Result{State: module.JobFailed, Error: fmt.Sprintf("invalid service name %q", p.Service)}, nil
		}
		if simulated() {
			return m.simulate(ctx, emit, "restarting "+p.Service+" (simulated)", p.Service+" restarted")
		}
		argv, err := restartArgv(runtime.GOOS, hasSystemctl(), p.Service)
		if err != nil {
			return module.Result{State: module.JobFailed, Error: err.Error()}, nil
		}
		return runReal(ctx, emit, "restarting "+p.Service, argv...)
	case "uptime":
		return dataResult(map[string]string{"output": strings.TrimSpace(runCmd(ctx, "uptime"))}), nil
	case "disk-usage":
		return dataResult(map[string]string{"output": strings.TrimSpace(runCmd(ctx, "df", "-h"))}), nil
	case "list-interfaces":
		return interfacesResult(), nil
	default:
		return module.Result{State: module.JobFailed, Error: "unknown action: " + req.Action}, nil
	}
}

func (m *Module) simulate(ctx context.Context, emit func(module.Event), startMsg, doneMsg string) (module.Result, error) {
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

// StreamLogs emits simulated system log lines until ctx is cancelled.
func (m *Module) StreamLogs(ctx context.Context, params json.RawMessage, emit func([]byte)) error {
	t := time.NewTicker(700 * time.Millisecond)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			n++
			emit([]byte(fmt.Sprintf("%s systemd[1]: log message %d", time.Now().Format(time.RFC3339), n)))
		}
	}
}

// hasSystemctl reports whether the host is managed by systemd. On Linux the associate installer
// supports systemd and OpenRC, so its absence there means OpenRC.
func hasSystemctl() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// rebootArgv is the command that reboots a host running goos.
func rebootArgv(goos string, systemctl bool) ([]string, error) {
	switch {
	case goos == "linux" && systemctl:
		return []string{"systemctl", "reboot"}, nil
	case goos == "linux":
		return []string{"reboot"}, nil
	case goos == "darwin":
		return []string{"shutdown", "-r", "now"}, nil
	}
	return nil, fmt.Errorf("reboot is not supported on %s", goos)
}

// restartArgv is the command that restarts the named service on a host running goos. On macOS
// the name is a launchd daemon label (e.g. com.docker.vmnetd).
func restartArgv(goos string, systemctl bool, service string) ([]string, error) {
	switch {
	case goos == "linux" && systemctl:
		return []string{"systemctl", "restart", service}, nil
	case goos == "linux":
		return []string{"rc-service", service, "restart"}, nil
	case goos == "darwin":
		return []string{"launchctl", "kickstart", "-k", "system/" + service}, nil
	}
	return nil, fmt.Errorf("restart-service is not supported on %s", goos)
}

// validService rejects names that systemctl / rc-service / launchctl would parse as something other than
// one service: a leading '-' (an option) or any whitespace or control character. No shell is
// involved, so nothing else needs escaping.
func validService(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// runReal executes a real command, streaming startMsg and then its output as log events.
func runReal(ctx context.Context, emit func(module.Event), startMsg string, argv ...string) (module.Result, error) {
	emit(module.Event{Kind: module.EventState, State: module.JobRunning})
	emit(module.Event{Kind: module.EventLog, Message: startMsg})
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
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
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	return module.Result{State: module.JobSucceeded}, nil
}

func runCmd(ctx context.Context, name string, args ...string) string {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("%s: %v", name, err)
	}
	return string(out)
}

func dataResult(v any) module.Result {
	b, _ := json.Marshal(v)
	return module.Result{State: module.JobSucceeded, Data: b}
}

func interfacesResult() module.Result {
	ifaces, err := net.Interfaces()
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}
	}
	type iface struct {
		Name  string   `json:"name"`
		MAC   string   `json:"mac,omitempty"`
		Addrs []string `json:"addrs,omitempty"`
	}
	var list []iface
	for _, in := range ifaces {
		entry := iface{Name: in.Name, MAC: in.HardwareAddr.String()}
		if addrs, err := in.Addrs(); err == nil {
			for _, a := range addrs {
				entry.Addrs = append(entry.Addrs, a.String())
			}
		}
		list = append(list, entry)
	}
	return dataResult(map[string]any{"interfaces": list})
}
