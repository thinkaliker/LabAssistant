package dashboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestJS runs the dashboard's JavaScript tests (dashboard/jstest, `node --test`) so `go test ./...`
// covers the compose editor's YAML engine too. They need Node 22.7+ (ES module detection for the
// plain .js files); without it the test is skipped rather than failed.
func TestJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Skipf("node --version: %v", err)
	}
	ver := strings.Split(strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), ".")
	major, _ := strconv.Atoi(ver[0])
	minor := 0
	if len(ver) > 1 {
		minor, _ = strconv.Atoi(ver[1])
	}
	if major < 22 || (major == 22 && minor < 7) {
		t.Skipf("node %s is too old for the JS tests (need 22.7+)", strings.TrimSpace(string(out)))
	}
	// go test caches a passing result by the files the test binary itself reads, and node's reads
	// don't count: read the sources here so editing a .js file re-runs the suite.
	for _, dir := range []string{"js", "jstest", "jstest/fixtures", "vendor"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				if _, err := os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	cmd := exec.Command(node, "--test", "jstest/")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node --test jstest/: %v\n%s", err, b)
	}
}
