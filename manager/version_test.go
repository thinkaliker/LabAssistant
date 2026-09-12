package manager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run executes a git command in dir, failing the test on error.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestProbeVersion(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")   // the manager's checkout
	pusher := filepath.Join(root, "push") // someone else landing commits on origin

	run(t, root, "init", "--bare", "-b", "main", origin)
	run(t, root, "clone", origin, pusher)
	run(t, pusher, "commit", "--allow-empty", "-m", "one")
	run(t, pusher, "push", "origin", "HEAD:main")
	run(t, root, "clone", origin, work)

	ctx := context.Background()
	v := probeVersion(ctx, work)
	if v.Error != "" {
		t.Fatalf("unexpected error: %s", v.Error)
	}
	if v.Branch != "main" || v.Local == "" || v.Local != v.Remote || v.UpdateAvailable {
		t.Fatalf("fresh clone should be up to date: %+v", v)
	}

	run(t, pusher, "commit", "--allow-empty", "-m", "two")
	run(t, pusher, "push", "origin", "HEAD:main")
	v = probeVersion(ctx, work)
	if v.Error != "" || !v.UpdateAvailable || v.Local == v.Remote {
		t.Fatalf("new commit on origin should show an update: %+v", v)
	}

	run(t, work, "checkout", "--detach")
	if v = probeVersion(ctx, work); v.Error == "" || v.UpdateAvailable {
		t.Fatalf("detached HEAD should report an error, not an update: %+v", v)
	}

	if v = probeVersion(ctx, root); v.Error == "" || v.UpdateAvailable {
		t.Fatalf("non-checkout should report an error: %+v", v)
	}
}
