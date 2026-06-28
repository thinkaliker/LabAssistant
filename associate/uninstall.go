package associate

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// systemdUnit is the service name the SSH installer creates (see quartermaster).
const systemdUnit = "labassistant-associate"

// selfUninstall removes the associate from the host: it launches a detached teardown that
// stops and disables the service and deletes the install directory, then exits this
// process. The teardown runs under setsid so stopping this process mid-removal cannot kill
// it. Only supported on Linux; elsewhere the associate just exits so the manager can drop
// the host record.
func (a *Associate) selfUninstall(reason string) {
	slog.Info("self-uninstall requested", "reason", reason)
	if runtime.GOOS != "linux" {
		slog.Warn("self-uninstall teardown only supported on linux; exiting")
		go exitSoon()
		return
	}

	installDir := "/opt/labassistant"
	if a.bundlePath != "" {
		installDir = filepath.Dir(a.bundlePath)
	}
	script := teardownScript(installDir)

	// Run the whole teardown as root (via sudo -n when unprivileged), detached from this
	// process so the service stop below doesn't abort it.
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command("setsid", "bash", "-c", script)
	} else {
		cmd = exec.Command("setsid", "sudo", "-n", "bash", "-c", script)
	}
	if err := cmd.Start(); err != nil {
		slog.Error("failed to launch teardown", "err", err)
	} else {
		_ = cmd.Process.Release()
	}
	go exitSoon()
}

// teardownScript stops + disables the systemd unit and removes installed files. The leading
// sleep lets this process flush its final messages before the service is stopped.
func teardownScript(installDir string) string {
	return strings.Join([]string{
		"sleep 1",
		"systemctl disable --now " + systemdUnit + " 2>/dev/null || true",
		"rm -f /etc/systemd/system/" + systemdUnit + ".service",
		"systemctl daemon-reload 2>/dev/null || true",
		"rm -rf " + installDir,
	}, "\n")
}

// exitSoon exits the process after a short grace so a non-systemd (dev) associate also
// stops when asked to uninstall.
func exitSoon() {
	time.Sleep(2 * time.Second)
	os.Exit(0)
}
