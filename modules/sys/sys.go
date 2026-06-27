// Package sys implements host-level system actions: reboot, restarting a system service,
// uptime, disk usage, and network-interface listing, plus streaming system logs. Actions
// are an explicit, enumerated set (never arbitrary command execution); privilege is
// declared per action.
//
// reboot and restart-service are SIMULATED in this build so development hosts are never
// actually rebooted. TODO(real): wire reboot -> `systemctl reboot` and restart-service ->
// `systemctl restart <unit>` via the privileged helper, gated behind explicit enablement.
package sys

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/thinkaliker/labassistant/module"
)

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
			{Name: "uptime", Description: "Show host uptime.", Privilege: module.PrivilegeNone, DefaultTimeout: 15 * time.Second},
			{Name: "disk-usage", Description: "Show disk usage.", Privilege: module.PrivilegeNone, DefaultTimeout: 15 * time.Second},
			{Name: "list-interfaces", Description: "List network interfaces.", Privilege: module.PrivilegeNone, DefaultTimeout: 15 * time.Second},
		},
	}
}

func (m *Module) Detect(ctx context.Context) (module.Detection, error) {
	return module.Detection{Applicable: true, Capabilities: map[string]string{"os": runtime.GOOS}}, nil
}

func (m *Module) Status(ctx context.Context) (module.Status, error) {
	out := strings.TrimSpace(runCmd(ctx, "uptime"))
	data, _ := json.Marshal(map[string]string{"uptime": out})
	return module.Status{Summary: out, Data: data}, nil
}

func (m *Module) Execute(ctx context.Context, req module.ActionRequest, emit func(module.Event)) (module.Result, error) {
	switch req.Action {
	case "reboot":
		return m.simulate(ctx, emit, "rebooting host (simulated)", "host would reboot now")
	case "restart-service":
		var p struct {
			Service string `json:"service"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.Service == "" {
			return module.Result{State: module.JobFailed, Error: "service is required"}, nil
		}
		return m.simulate(ctx, emit, "restarting "+p.Service+" (simulated)", p.Service+" restarted")
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
