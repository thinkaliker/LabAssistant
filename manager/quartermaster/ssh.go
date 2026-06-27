package quartermaster

import (
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

// SSHInstaller installs the associate on a remote host over SSH: it uploads the associate
// binary and enrollment bundle via SFTP and starts the agent. Host keys are verified
// trust-on-first-use (see hostKeyCallback).
type SSHInstaller struct {
	AssociateBin   string // local path to the associate binary to upload
	HelperBin      string // optional local path to the associatehelper binary
	RemoteDir      string // remote install directory (default: labassistant)
	Port           int    // SSH port (default 22)
	KnownHostsPath string // path to the TOFU known-hosts file
}

// Install connects over SSH, uploads the agent and bundle, and starts it.
func (s SSHInstaller) Install(ctx context.Context, p InstallParams, emit func(string)) error {
	port := s.Port
	if port == 0 {
		port = 22
	}
	remoteDir := s.RemoteDir
	if remoteDir == "" {
		remoteDir = "labassistant"
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
		return fmt.Errorf("no SSH auth method (provide a password)")
	}

	addr := net.JoinHostPort(p.IP, strconv.Itoa(port))
	emit("ssh dial " + addr)
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	sc, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	defer sc.Close()

	if err := sc.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("mkdir %s: %w", remoteDir, err)
	}
	emit("uploading associate binary")
	if err := uploadFile(sc, s.AssociateBin, path.Join(remoteDir, "associate"), 0o755); err != nil {
		return err
	}
	helperArg := ""
	if s.HelperBin != "" {
		if err := uploadFile(sc, s.HelperBin, path.Join(remoteDir, "associatehelper"), 0o755); err != nil {
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

	emit("installing systemd service")
	if err := sshRun(client, installScript(remoteDir, helperArg)); err != nil {
		return fmt.Errorf("install service: %w", err)
	}
	return nil
}

const installDir = "/opt/labassistant"

// installScript moves the staged files into installDir and installs a systemd unit that
// restarts the associate on failure and across reboots. It requires passwordless sudo for
// the SSH user (typical for a homelab control account).
func installScript(stageDir, helperArg string) string {
	execStart := installDir + "/associate --bundle " + installDir + "/bundle.json" + helperArg
	unit := "[Unit]\n" +
		"Description=LabAssistant associate\n" +
		"After=network-online.target\nWants=network-online.target\n\n" +
		"[Service]\n" +
		"ExecStart=" + execStart + "\n" +
		"WorkingDirectory=" + installDir + "\n" +
		"Restart=always\nRestartSec=5\n\n" +
		"[Install]\nWantedBy=multi-user.target\n"
	return strings.Join([]string{
		"set -e",
		"sudo mkdir -p " + installDir,
		fmt.Sprintf("sudo cp %s/associate %s/", stageDir, installDir),
		fmt.Sprintf("[ -f %s/associatehelper ] && sudo cp %s/associatehelper %s/ || true", stageDir, stageDir, installDir),
		fmt.Sprintf("sudo cp %s/bundle.json %s/ && sudo chmod 600 %s/bundle.json", stageDir, installDir, installDir),
		"sudo tee /etc/systemd/system/labassistant-associate.service >/dev/null <<'UNIT'\n" + unit + "UNIT",
		"sudo systemctl daemon-reload",
		"sudo systemctl enable --now labassistant-associate",
		"rm -rf " + stageDir,
	}, "\n")
}

func sshRun(client *ssh.Client, script string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	return sess.Run("bash -c " + shellQuote(script))
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
