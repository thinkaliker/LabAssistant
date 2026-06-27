package quartermaster

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	dir := filepath.Join(l.WorkDir, p.HostID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	bundlePath := filepath.Join(dir, "bundle.json")
	if err := p.Bundle.Save(bundlePath); err != nil {
		return err
	}
	emit("wrote bundle to " + bundlePath)

	args := []string{"--bundle", bundlePath}
	if l.HelperBin != "" {
		args = append(args, "--helper", l.HelperBin)
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
	emit("started local associate (pid " + strconv.Itoa(cmd.Process.Pid) + ")")
	go func() {
		_ = cmd.Wait()
		logFile.Close()
	}()
	return nil
}
