package quartermaster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SSHInstaller installs and removes the associate on a remote host over SSH. On install it
// detects the host OS/arch and init system, uploads the matching associate binary and the
// enrollment bundle via SFTP, and registers a service. Host keys are verified
// trust-on-first-use (see hostKeyCallback).
//
// AssociateBin / HelperBin may contain {os} and {arch} placeholders (e.g.
// "/srv/bin/associate-{os}-{arch}") so the installer can pick the build matching the
// detected host platform.
type SSHInstaller struct {
	AssociateBin   string // local path or {os}/{arch} template for the associate binary
	HelperBin      string // optional local path or template for the associatehelper binary
	RemoteDir      string // remote staging directory (default: labassistant)
	Port           int    // SSH port (default 22)
	KnownHostsPath string // path to the TOFU known-hosts file
}

const installDir = "/opt/labassistant"

// dial opens an SSH connection to the host using TOFU host-key verification.
func (s SSHInstaller) dial(p InstallParams) (*ssh.Client, error) {
	port := s.Port
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User:            p.SSHUser,
		HostKeyCallback: s.hostKeyCallback(),
		Timeout:         15 * time.Second,
	}
	if p.SSHPassword != "" {
		cfg.Auth = append(cfg.Auth, ssh.Password(p.SSHPassword))
	}
	if len(cfg.Auth) == 0 {
		return nil, fmt.Errorf("no SSH auth method (provide a password)")
	}
	addr := net.JoinHostPort(p.IP, strconv.Itoa(port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return client, nil
}

// Install connects over SSH, uploads the agent and bundle, and starts the service.
func (s SSHInstaller) Install(ctx context.Context, p InstallParams, emit func(string)) error {
	if s.AssociateBin == "" {
		return fmt.Errorf("no associate binary configured (set enroll.associate_bin in config.toml)")
	}
	remoteDir := s.RemoteDir
	if remoteDir == "" {
		remoteDir = "labassistant"
	}

	emit("ssh dial " + p.IP)
	client, err := s.dial(p)
	if err != nil {
		return err
	}
	defer client.Close()

	goos, goarch := detectPlatform(client, emit)
	initSys := detectInit(client)
	if initSys == "" {
		return fmt.Errorf("no supported service manager found on host (need systemd or openrc)")
	}

	associateBin := resolveBinary(s.AssociateBin, goos, goarch)
	if _, err := os.Stat(associateBin); err != nil {
		return fmt.Errorf("associate binary %q not found: %w", associateBin, err)
	}
	sc, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	defer sc.Close()

	if err := sc.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("mkdir %s: %w", remoteDir, err)
	}
	emit("uploading associate binary (" + associateBin + ")")
	if err := uploadFile(sc, associateBin, path.Join(remoteDir, "associate"), 0o755); err != nil {
		return err
	}
	helperArg := ""
	if s.HelperBin != "" {
		helperBin := resolveBinary(s.HelperBin, goos, goarch)
		if err := uploadFile(sc, helperBin, path.Join(remoteDir, "associatehelper"), 0o755); err != nil {
			return err
		}
		helperArg = " --helper " + installDir + "/associatehelper --sudo"
	}
	emit("uploading bundle")
	bundleBytes, err := json.Marshal(p.Bundle)
	if err != nil {
		return err
	}
	if err := writeRemote(sc, path.Join(remoteDir, "bundle.json"), bundleBytes, 0o600); err != nil {
		return err
	}

	emit("installing " + initSys + " service")
	if err := sshRun(client, installScript(remoteDir, helperArg, initSys), p.SSHPassword); err != nil {
		return fmt.Errorf("install service: %w", err)
	}
	return nil
}

// Uninstall connects over SSH and removes the associate (stops + disables the service and
// deletes the install directory). Used as the fallback teardown when a host is offline and
// cannot self-uninstall over the stream.
func (s SSHInstaller) Uninstall(ctx context.Context, p InstallParams, emit func(string)) error {
	emit("ssh dial " + p.IP)
	client, err := s.dial(p)
	if err != nil {
		return err
	}
	defer client.Close()

	initSys := detectInit(client)
	emit("removing associate service")
	if err := sshRun(client, uninstallScript(initSys), p.SSHPassword); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	emit("associate removed from host")
	return nil
}

// Revive connects over SSH and makes sure the already-installed associate service is enabled
// and running: it (re-)enables it on boot and starts it if stopped. Used to recover a host
// whose associate did not come back after a reboot (service disabled, or crashed and not
// restarted). It does not upload binaries or a bundle — the install must already exist.
func (s SSHInstaller) Revive(ctx context.Context, p InstallParams, emit func(string)) error {
	emit("ssh dial " + p.IP)
	client, err := s.dial(p)
	if err != nil {
		return err
	}
	defer client.Close()

	if out, _ := sshOutput(client, "[ -x "+installDir+"/associate ] && echo yes || true"); out != "yes" {
		return fmt.Errorf("associate is not installed at %s (enroll the host first)", installDir)
	}
	initSys := detectInit(client)
	if initSys == "" {
		return fmt.Errorf("no supported service manager found on host (need systemd or openrc)")
	}
	emit("enabling and starting associate service (" + initSys + ")")
	if err := sshRun(client, reviveScript(initSys), p.SSHPassword); err != nil {
		return fmt.Errorf("revive: %w", err)
	}
	status, _ := sshOutput(client, statusCmd(initSys))
	if status != "" {
		emit("service status: " + status)
	}
	return nil
}

// reviveScript re-enables the associate on boot and starts it if it is not already running.
func reviveScript(initSys string) string {
	if initSys == "openrc" {
		return strings.Join([]string{
			"set -e",
			"sudo rc-update add " + unitName + " default 2>/dev/null || true",
			"sudo rc-service " + unitName + " start 2>/dev/null || sudo rc-service " + unitName + " restart",
		}, "\n")
	}
	return strings.Join([]string{
		"set -e",
		"sudo systemctl enable " + unitName + " 2>/dev/null || true",
		"sudo systemctl start " + unitName,
	}, "\n")
}

// statusCmd returns a command that prints a one-line liveness summary for the service.
func statusCmd(initSys string) string {
	if initSys == "openrc" {
		return "sudo rc-service " + unitName + " status 2>/dev/null | head -1 || true"
	}
	return "sudo systemctl is-active " + unitName + " 2>/dev/null || true"
}

// resolveBinary substitutes {os}/{arch} placeholders in a binary path template.
func resolveBinary(tmpl, goos, goarch string) string {
	return strings.NewReplacer("{os}", goos, "{arch}", goarch).Replace(tmpl)
}

// detectPlatform maps the host's `uname` output to Go's GOOS/GOARCH. Defaults to
// linux/amd64 when detection fails.
func detectPlatform(client *ssh.Client, emit func(string)) (goos, goarch string) {
	goos, goarch = "linux", "amd64"
	if s, err := sshOutput(client, "uname -s"); err == nil {
		switch strings.ToLower(s) {
		case "linux":
			goos = "linux"
		case "darwin":
			goos = "darwin"
		case "freebsd":
			goos = "freebsd"
		}
	}
	if m, err := sshOutput(client, "uname -m"); err == nil {
		switch m {
		case "x86_64", "amd64":
			goarch = "amd64"
		case "aarch64", "arm64":
			goarch = "arm64"
		case "armv7l", "armv6l", "arm":
			goarch = "arm"
		case "i386", "i686":
			goarch = "386"
		}
	}
	emit(fmt.Sprintf("detected host platform %s/%s", goos, goarch))
	return goos, goarch
}

// detectInit reports the host's service manager: "systemd", "openrc", or "" if neither.
func detectInit(client *ssh.Client) string {
	if out, _ := sshOutput(client, "command -v systemctl || true"); out != "" {
		return "systemd"
	}
	if out, _ := sshOutput(client, "command -v rc-update || true"); out != "" {
		return "openrc"
	}
	return ""
}

const unitName = "labassistant-associate"

// installScript stages files into installDir and registers a service for the detected init
// system. It requires passwordless sudo for the SSH user (typical for a homelab control
// account).
func installScript(stageDir, helperArg, initSys string) string {
	execStart := installDir + "/associate --bundle " + installDir + "/bundle.json" + helperArg
	stage := []string{
		"set -e",
		"sudo mkdir -p " + installDir,
		fmt.Sprintf("sudo cp %s/associate %s/", stageDir, installDir),
		fmt.Sprintf("[ -f %s/associatehelper ] && sudo cp %s/associatehelper %s/ || true", stageDir, stageDir, installDir),
		fmt.Sprintf("sudo cp %s/bundle.json %s/ && sudo chmod 600 %s/bundle.json", stageDir, installDir, installDir),
	}
	var reg []string
	if initSys == "openrc" {
		reg = openrcRegister(execStart)
	} else {
		reg = systemdRegister(execStart)
	}
	tail := []string{"rm -rf " + stageDir}
	return strings.Join(append(append(stage, reg...), tail...), "\n")
}

func systemdRegister(execStart string) []string {
	unit := "[Unit]\n" +
		"Description=LabAssistant associate\n" +
		"After=network-online.target\nWants=network-online.target\n\n" +
		"[Service]\n" +
		"ExecStart=" + execStart + "\n" +
		"WorkingDirectory=" + installDir + "\n" +
		"Restart=always\nRestartSec=5\n\n" +
		"[Install]\nWantedBy=multi-user.target\n"
	return []string{
		"sudo tee /etc/systemd/system/" + unitName + ".service >/dev/null <<'UNIT'\n" + unit + "UNIT",
		"sudo systemctl daemon-reload",
		"sudo systemctl enable --now " + unitName,
	}
}

func openrcRegister(execStart string) []string {
	// command_args excludes the binary itself (the first token of execStart).
	bin, args := splitExec(execStart)
	script := "#!/sbin/openrc-run\n" +
		"description=\"LabAssistant associate\"\n" +
		"command=\"" + bin + "\"\n" +
		"command_args=\"" + args + "\"\n" +
		"command_background=true\n" +
		"directory=\"" + installDir + "\"\n" +
		"pidfile=\"/run/" + unitName + ".pid\"\n" +
		"output_log=\"/var/log/" + unitName + ".log\"\n" +
		"error_log=\"/var/log/" + unitName + ".log\"\n" +
		"depend() { need net; }\n"
	return []string{
		"sudo tee /etc/init.d/" + unitName + " >/dev/null <<'UNIT'\n" + script + "UNIT",
		"sudo chmod +x /etc/init.d/" + unitName,
		"sudo rc-update add " + unitName + " default",
		"sudo rc-service " + unitName + " restart",
	}
}

// uninstallScript stops and removes the associate service and its files for the given init
// system. initSys may be "" (host unreachable for detection) in which case both managers
// are attempted best-effort.
func uninstallScript(initSys string) string {
	lines := []string{"set +e"}
	if initSys == "" || initSys == "systemd" {
		lines = append(lines,
			"sudo systemctl disable --now "+unitName+" 2>/dev/null",
			"sudo rm -f /etc/systemd/system/"+unitName+".service",
			"sudo systemctl daemon-reload 2>/dev/null",
		)
	}
	if initSys == "" || initSys == "openrc" {
		lines = append(lines,
			"sudo rc-service "+unitName+" stop 2>/dev/null",
			"sudo rc-update del "+unitName+" default 2>/dev/null",
			"sudo rm -f /etc/init.d/"+unitName,
		)
	}
	lines = append(lines, "sudo rm -rf "+installDir, "true")
	return strings.Join(lines, "\n")
}

// splitExec splits an ExecStart line into its binary and the remaining argument string.
func splitExec(execStart string) (bin, args string) {
	parts := strings.SplitN(strings.TrimSpace(execStart), " ", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

// sshRun runs script on the host via `bash -c`. sudoPass, when non-empty, primes sudo's
// credential cache from stdin so the script's `sudo` calls work on hosts that require a
// password (i.e. without passwordless/NOPASSWD sudo); it is never written to disk or argv.
func sshRun(client *ssh.Client, script, sudoPass string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	if sudoPass != "" {
		// Validate+cache sudo once, reading the password from stdin. Later `sudo` calls in
		// the script reuse the cached timestamp. `-p ''` suppresses the prompt text.
		sess.Stdin = strings.NewReader(sudoPass + "\n")
		script = "sudo -S -p '' -v || { echo 'sudo authentication failed (check the SSH/sudo password, or grant passwordless sudo)' >&2; exit 1; }\n" + script
	}
	var out bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &out
	if err := sess.Run("bash -c " + shellQuote(script)); err != nil {
		if msg := lastLines(out.String(), 4); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// lastLines returns up to the last n non-empty lines of s, joined with "; ". Used to
// surface the tail of a failed remote script (the actual error) instead of a bare exit code.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// sshOutput runs a command and returns its trimmed stdout.
func sshOutput(client *ssh.Client, cmd string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.Output(cmd)
	return strings.TrimSpace(string(out)), err
}

func uploadFile(sc *sftp.Client, localPath, remotePath string, mode os.FileMode) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer src.Close()
	dst, err := sc.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create %s: %w", remotePath, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return sc.Chmod(remotePath, mode)
}

func writeRemote(sc *sftp.Client, remotePath string, data []byte, mode os.FileMode) error {
	dst, err := sc.Create(remotePath)
	if err != nil {
		return err
	}
	if _, err := dst.Write(data); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return sc.Chmod(remotePath, mode)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
