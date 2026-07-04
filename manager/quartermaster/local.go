package quartermaster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// LocalInstaller is a development installer: instead of SSHing to a remote host, it writes
// the bundle locally and spawns the associate as a child process. This lets the async
// Add Host flow be exercised on a single machine.
type LocalInstaller struct {
	AssociateBin string // path to the associate binary
	HelperBin    string // optional path to the associatehelper binary
	WorkDir      string // base directory for per-host bundles and logs
}

// Install writes the bundle and starts a local associate.
func (l LocalInstaller) Install(ctx context.Context, p InstallParams, emit func(string)) error {
	if l.AssociateBin == "" {
		return fmt.Errorf("no associate binary configured (set enroll.associate_bin in config.toml)")
	}
	if _, err := os.Stat(l.AssociateBin); err != nil {
		return fmt.Errorf("associate binary %q not found: %w", l.AssociateBin, err)
	}
	dir := filepath.Join(l.WorkDir, p.HostID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	bundlePath := filepath.Join(dir, "bundle.json")
	if err := p.Bundle.Save(bundlePath); err != nil {
		return err
	}
	emit("wrote bundle to " + bundlePath)
	return l.spawn(dir, bundlePath, emit)
}

// Revive re-starts a local associate child that has exited (the dev-mode analogue of
// re-enabling and starting the systemd service). If the child is still running it is a
// no-op. Requires an existing install (bundle on disk).
func (l LocalInstaller) Revive(ctx context.Context, p InstallParams, emit func(string)) error {
	dir := filepath.Join(l.WorkDir, p.HostID)
	bundlePath := filepath.Join(dir, "bundle.json")
	if _, err := os.Stat(bundlePath); err != nil {
		return fmt.Errorf("associate is not installed locally (no bundle at %s)", bundlePath)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "associate.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && processAlive(pid) {
			emit("local associate already running (pid " + strconv.Itoa(pid) + ")")
			return nil
		}
	}
	emit("local associate not running; restarting")
	return l.spawn(dir, bundlePath, emit)
}

// spawn starts the associate as a detached child logging to associate.log and records its pid.
func (l LocalInstaller) spawn(dir, bundlePath string, emit func(string)) error {
	args := []string{"--bundle", bundlePath}
	if l.HelperBin != "" {
		// Match the SSH installer: run elevated actions through sudo so the manager's sudo
		// password prompt flow applies (passwordless sudo still works via `sudo -n`).
		args = append(args, "--helper", l.HelperBin, "--sudo")
	}
	cmd := exec.Command(l.AssociateBin, args...)
	logFile, err := os.Create(filepath.Join(dir, "associate.log"))
	if err != nil {
		return err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}
	pid := cmd.Process.Pid
	_ = os.WriteFile(filepath.Join(dir, "associate.pid"), []byte(strconv.Itoa(pid)), 0o600)
	emit("started local associate (pid " + strconv.Itoa(pid) + ")")
	go func() {
		_ = cmd.Wait()
		logFile.Close()
	}()
	return nil
}

// processAlive reports whether a process with the given pid exists (signal 0 probe).
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// Uninstall stops a local associate child and removes its working directory. Used as the
// offline-teardown fallback in development.
func (l LocalInstaller) Uninstall(ctx context.Context, p InstallParams, emit func(string)) error {
	dir := filepath.Join(l.WorkDir, p.HostID)
	if b, err := os.ReadFile(filepath.Join(dir, "associate.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Signal(syscall.SIGTERM)
				emit("stopped local associate (pid " + strconv.Itoa(pid) + ")")
			}
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	emit("removed local associate working directory")
	return nil
}
