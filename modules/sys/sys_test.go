package sys

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/thinkaliker/labassistant/internal/elevated"
)

// TestSimulatedOnlyByOptIn: the OS must not decide this — a Mac can be a real host — so only
// the explicit opt-out simulates, and the dashboard's mode reflects it.
func TestSimulatedOnlyByOptIn(t *testing.T) {
	for _, c := range []struct {
		env  string
		want bool
	}{{"", false}, {"0", false}, {"true", false}, {"1", true}} {
		t.Setenv(SimulateEnv, c.env)
		if got := simulated(); got != c.want {
			t.Errorf("%s=%q: simulated() = %v, want %v", SimulateEnv, c.env, got, c.want)
		}
	}
	t.Setenv(SimulateEnv, "1")
	if m := mode(); m != "simulated" {
		t.Errorf("mode() = %q with the opt-out set", m)
	}
	t.Setenv(SimulateEnv, "")
	if m := mode(); m != "real" {
		t.Errorf("mode() = %q on %s without the opt-out, want real", m, runtime.GOOS)
	}
}

func TestRebootAndRestartArgv(t *testing.T) {
	for _, c := range []struct {
		goos            string
		systemctl       bool
		reboot, restart string
	}{
		{"linux", true, "systemctl reboot", "systemctl restart nginx"},
		{"linux", false, "reboot", "rc-service nginx restart"},
		{"darwin", false, "shutdown -r now", "launchctl kickstart -k system/nginx"},
	} {
		r, err := rebootArgv(c.goos, c.systemctl)
		if err != nil || strings.Join(r, " ") != c.reboot {
			t.Errorf("rebootArgv(%s, %v) = %v, %v; want %q", c.goos, c.systemctl, r, err, c.reboot)
		}
		s, err := restartArgv(c.goos, c.systemctl, "nginx")
		if err != nil || strings.Join(s, " ") != c.restart {
			t.Errorf("restartArgv(%s, %v) = %v, %v; want %q", c.goos, c.systemctl, s, err, c.restart)
		}
	}
	// An OS with no known command fails loudly instead of quietly simulating.
	if _, err := rebootArgv("windows", false); err == nil {
		t.Error("rebootArgv(windows) succeeded")
	}
	if _, err := restartArgv("freebsd", false, "nginx"); err == nil {
		t.Error("restartArgv(freebsd) succeeded")
	}
}

// TestSimulateEnvForwarded: the opt-out only protects a host if the privileged helper sees it
// through sudo's env reset.
func TestSimulateEnvForwarded(t *testing.T) {
	if !slices.Contains(elevated.ForwardedEnv, SimulateEnv) {
		t.Fatalf("%s missing from elevated.ForwardedEnv %v", SimulateEnv, elevated.ForwardedEnv)
	}
}

func TestValidService(t *testing.T) {
	for _, ok := range []string{"nginx", "docker.service", "getty@tty1.service", `systemd-fsck@dev-disk-by\x2duuid-1.service`, "net.eth0"} {
		if !validService(ok) {
			t.Errorf("validService(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "-H", "--root=/tmp", "a b", "nginx\n", "x\tY"} {
		if validService(bad) {
			t.Errorf("validService(%q) = true, want false", bad)
		}
	}
}
