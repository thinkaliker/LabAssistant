package manager

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thinkaliker/labassistant/internal/build"
	"github.com/thinkaliker/labassistant/manager/api"
)

// versionCacheTTL is how long a remote lookup is reused. Reaching the remote costs an SSH
// handshake to GitHub, and the dashboard asks on every visit to the settings page, so an
// uncached check would open a connection per page view for an answer that changes rarely.
const versionCacheTTL = 10 * time.Minute

// gitTimeout bounds every git invocation. ls-remote talks to the network, and a manager that
// blocks an API handler on an unreachable remote is worse than one that reports "unknown".
const gitTimeout = 20 * time.Second

// versionCache holds the last completed check. A failed check is cached too (for a shorter
// window) so an unreachable remote doesn't mean an SSH attempt per request.
type versionCache struct {
	mu   sync.Mutex
	info api.ManagerVersion
	at   time.Time
}

// managerVersion reports what this manager is running against what the remote holds, so the
// dashboard can say whether an update is waiting before the user triggers a self-update.
//
// The comparison is done with the checkout's own git remote rather than GitHub's HTTP API:
// it is the same remote (and the same credentials) `manage.sh update` would pull from, so the
// answer matches what an update would actually do — and it stays correct for a fork, a private
// repo, or a mirror. ls-remote is read-only: nothing is fetched or written into the checkout.
func (a *App) managerVersion(ctx context.Context, force bool) api.ManagerVersion {
	a.verCache.mu.Lock()
	defer a.verCache.mu.Unlock()

	ttl := versionCacheTTL
	if a.verCache.info.Error != "" {
		ttl = time.Minute // retry a failed check sooner than a good one
	}
	if !force && !a.verCache.at.IsZero() && time.Since(a.verCache.at) < ttl {
		return a.verCache.info
	}

	// Detached from the request: the result is cached and shared, so a browser that navigates
	// away mid-check must not leave every later caller a cached "timed out".
	info := probeVersion(context.WithoutCancel(ctx), a.checkoutDir())
	a.verCache.info = info
	a.verCache.at = time.Now()
	return info
}

// probeVersion does the actual git work on the checkout at dir: what it has, what the remote has.
func probeVersion(ctx context.Context, dir string) api.ManagerVersion {
	info := api.ManagerVersion{
		Running:   build.Revision(),
		CheckedAt: time.Now().UTC(),
	}
	if dir == "" || !fileExists(filepath.Join(dir, ".git")) {
		info.Error = "not running from a git checkout, so there is nothing to compare"
		return info
	}
	local, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Local = local

	// A detached HEAD reports the branch as "HEAD", which names no remote branch; there is no
	// upstream to compare against, so say so rather than silently comparing with the default.
	branch, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		info.Error = err.Error()
		return info
	}
	if branch == "HEAD" {
		info.Error = "checkout is on a detached HEAD, so it tracks no branch"
		return info
	}
	info.Branch = branch

	remote, err := gitRemoteHead(ctx, dir, branch)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Remote = remote
	info.UpdateAvailable = remote != local
	return info
}

// gitRemoteHead asks the remote what it has at the tip of branch, without fetching.
func gitRemoteHead(ctx context.Context, dir, branch string) (string, error) {
	out, err := git(ctx, dir, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("origin has no branch %q", branch)
	}
	// "<sha>\trefs/heads/<branch>" — one line, since the ref was named exactly.
	sha, _, ok := strings.Cut(out, "\t")
	if !ok || len(sha) < 7 {
		return "", fmt.Errorf("unexpected ls-remote output from origin")
	}
	return sha, nil
}

// git runs one read-only git command in the checkout and returns its trimmed stdout. A check
// must never hang on a prompt: an HTTPS remote missing a credential is stopped by
// GIT_TERMINAL_PROMPT=0, and an SSH key wanting a passphrase finds no controlling terminal
// in the new session, so ssh fails instead of asking. The timeout backstops anything else.
// GIT_SSH_COMMAND is deliberately left alone — setting it would override a core.sshCommand
// the checkout relies on to reach its remote.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(updateEnv(), "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s timed out", args[0])
		}
		return "", fmt.Errorf("git %s failed: %s", args[0], gitErrText(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// gitErrText prefers git's own stderr over the bare "exit status 128", which tells the user
// nothing about whether the remote was unreachable, the key was rejected, or the ref is gone.
func gitErrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return firstLine(msg)
		}
	}
	return err.Error()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
