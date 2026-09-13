package duo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/thinkaliker/labassistant/module"
)

// The project .env is the file docker compose reads for ${VAR} interpolation. It lives in the
// project directory, which for a stack started with `docker compose -f <file>` is the directory
// of that (first) compose file, so that is where read-env and write-env look.

// maxEnvBytes caps the .env content read-env returns and write-env accepts. It stays far below
// the helper's 1 MiB result frame (internal/elevated) even after JSON escaping.
const maxEnvBytes = 256 << 10

// errEnvChanged is returned when the file on disk no longer matches what the editor loaded.
var errEnvChanged = errors.New(".env changed on the host since it was opened; reopen it and reapply your edits")

// envValidator checks a candidate env file against the stack's compose files, returning the
// tool's combined output. A seam so tests don't need docker.
type envValidator func(ctx context.Context, files []string, envFile string) ([]byte, error)

// validateComposeEnv validates the stack with envFile standing in for the project .env.
// --env-file replaces the default .env lookup, so the live file is never involved.
func validateComposeEnv(ctx context.Context, files []string, envFile string) ([]byte, error) {
	args := []string{"compose"}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	args = append(args, "--env-file", envFile, "config", "-q")
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// envInfo locates a project .env.
type envInfo struct {
	Path       string // dir/.env: the name compose reads
	Target     string // the regular file actually read and written (Path, or where its symlink points)
	Exists     bool
	OutsideDir bool // Target is a symlinked file outside the stack directory (e.g. a shared .env)
}

// resolveEnv finds dir/.env, following a symlink to its target. A shared .env symlinked from
// several stacks is a common layout, so links are followed rather than refused; OutsideDir lets
// the dashboard say so. Anything that isn't a regular file (a directory, a dangling link) is an
// error, since writing to it would not do what the user expects.
func resolveEnv(dir string) (envInfo, error) {
	info := envInfo{Path: filepath.Join(dir, ".env")}
	info.Target = info.Path
	fi, err := os.Lstat(info.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(info.Path)
		if err != nil {
			return info, fmt.Errorf("%s is a symlink that can't be resolved: %w", info.Path, err)
		}
		info.Target = target
		if fi, err = os.Stat(target); err != nil {
			return info, err
		}
		// Compare resolved paths: the stack directory itself may sit behind a symlink.
		realDir, derr := filepath.EvalSymlinks(dir)
		if derr != nil {
			realDir = dir
		}
		info.OutsideDir = !within(realDir, target)
	}
	if !fi.Mode().IsRegular() {
		return info, fmt.Errorf("%s is not a regular file", info.Target)
	}
	info.Exists = true
	return info, nil
}

// within reports whether path lies inside dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// envRead is the outcome of readEnvAt.
type envRead struct {
	envInfo
	Content   []byte
	Truncated bool
	// SHA256 hashes the whole file (empty content when it doesn't exist), so a later write can
	// tell whether the file changed underneath the editor — including being created or deleted.
	SHA256 string
}

// readEnvAt reads dir's project .env. A missing file is not an error.
func readEnvAt(dir string, limit int) (envRead, error) {
	info, err := resolveEnv(dir)
	r := envRead{envInfo: info, SHA256: sha256Hex(nil)}
	if err != nil || !info.Exists {
		return r, err
	}
	b, err := os.ReadFile(info.Target)
	if err != nil {
		return r, err
	}
	r.SHA256 = sha256Hex(b)
	if len(b) > limit {
		b, r.Truncated = b[:limit], true
	}
	r.Content = b
	return r, nil
}

// envWriteReport describes what writeEnvAt did.
type envWriteReport struct {
	Target   string
	Created  bool
	BackedUp bool
	SHA256   string
}

// writeEnvAt replaces dir's project .env with content. The candidate is validated from a temp
// file before the live file is touched, so a rejected edit leaves nothing to restore. An existing
// file is backed up to <target>.bak and rewritten in place (keeping its inode, mode and owner); a
// new one is created 0600 — .env files usually hold secrets — owned like the compose file.
//
// baseSum, when set, must match the current file's sha256 (see envRead.SHA256).
func writeEnvAt(ctx context.Context, dir string, files []string, content []byte, baseSum string,
	validate envValidator, logf func(string)) (envWriteReport, error) {
	var rep envWriteReport
	if len(content) > maxEnvBytes {
		return rep, fmt.Errorf(".env content is %d bytes; the limit is %d", len(content), maxEnvBytes)
	}
	if len(files) == 0 {
		return rep, errors.New("no compose file to validate against")
	}
	info, err := resolveEnv(dir)
	if err != nil {
		return rep, err
	}
	rep.Target = info.Target
	var cur []byte
	if info.Exists {
		if cur, err = os.ReadFile(info.Target); err != nil {
			return rep, err
		}
	}
	if baseSum != "" && baseSum != sha256Hex(cur) {
		return rep, errEnvChanged
	}

	tmp, err := os.CreateTemp("", "la-env-*") // created 0600
	if err != nil {
		return rep, err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(content)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return rep, werr
	}
	if out, verr := validate(ctx, files, tmp.Name()); verr != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = verr.Error()
		}
		// Compose names the file it parsed; report the real one, and never echo values back into
		// job records.
		msg = strings.ReplaceAll(msg, tmp.Name(), info.Path)
		return rep, fmt.Errorf("compose validation failed: %s", scrubEnvValues(msg, content))
	}
	logf("validated with docker compose config")

	if !info.Exists {
		f, err := os.OpenFile(info.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return rep, fmt.Errorf("create %s: %w", info.Path, err)
		}
		if err := writeClose(f, content); err != nil {
			return rep, fmt.Errorf("write %s: %w", info.Path, err)
		}
		// Only takes effect when running as root (the helper), which is exactly when the file
		// would otherwise end up root-owned and unreadable to whoever runs compose by hand.
		if fi, err := os.Stat(files[0]); err == nil {
			copyOwner(info.Path, fi)
		}
		rep.Created = true
		logf("created " + info.Path + " (mode 0600)")
	} else {
		fi, err := os.Stat(info.Target)
		if err != nil {
			return rep, err
		}
		bak := info.Target + ".bak"
		// O_NOFOLLOW: a planted .bak symlink must not redirect a root write. Chmod afterwards,
		// because an existing .bak keeps whatever (possibly wider) mode it had.
		bf, err := os.OpenFile(bak, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, fi.Mode().Perm())
		if err != nil {
			return rep, fmt.Errorf("write backup: %w", err)
		}
		_ = bf.Chmod(fi.Mode().Perm())
		if err := writeClose(bf, cur); err != nil {
			return rep, fmt.Errorf("write backup: %w", err)
		}
		copyOwner(bak, fi)
		rep.BackedUp = true
		logf("backed up to " + bak)

		f, err := os.OpenFile(info.Target, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return rep, fmt.Errorf("write %s: %w", info.Target, err)
		}
		if err := writeClose(f, content); err != nil {
			return rep, fmt.Errorf("write %s: %w", info.Target, err)
		}
		logf("wrote " + info.Target)
	}
	rep.SHA256 = sha256Hex(content)
	return rep, nil
}

func writeClose(f *os.File, b []byte) error {
	_, err := f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// copyOwner gives path the uid/gid of fi, ignoring failure (unprivileged callers can't chown).
func copyOwner(path string, fi os.FileInfo) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(path, int(st.Uid), int(st.Gid))
	}
}

// scrubEnvValues masks, in text, every value from content's KEY=VALUE lines. Compose's parse
// errors quote the offending value (e.g. `unterminated quoted value "hunter2`), and job errors are
// kept by the manager and shown in the dashboard. Continuation lines of multi-line values are
// masked whole. Short values (under 4 bytes) are left alone: masking them mangles the message and
// they are rarely secrets.
func scrubEnvValues(text string, content []byte) string {
	var vals []string
	add := func(v string) {
		if len(v) >= 4 {
			vals = append(vals, v)
		}
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.TrimSpace(strings.TrimPrefix(line, "export "))
		if i := strings.IndexAny(kv, "=:"); i > 0 && !strings.ContainsAny(kv[:i], " \t\"'") {
			v := strings.TrimSpace(kv[i+1:])
			add(v)
			add(strings.Trim(v, `"'`))
			continue
		}
		add(line)
		add(strings.Trim(line, `"'`)) // the closing line of a multi-line quoted value
	}
	// Longest first, so a value that contains another is masked whole.
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		text = strings.ReplaceAll(text, v, "****")
	}
	return text
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readEnv returns a stack's project .env in Result.Data.
func (m *Module) readEnv(ctx context.Context, p actionParams) (module.Result, error) {
	files, err := m.composeFiles(ctx, p.Stack)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	r, err := readEnvAt(filepath.Dir(files[0]), maxEnvBytes)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	data, _ := json.Marshal(map[string]any{
		"stack": p.Stack, "path": r.Path, "target": r.Target, "outsideDir": r.OutsideDir,
		"content": string(r.Content), "exists": r.Exists, "truncated": r.Truncated, "sha256": r.SHA256,
	})
	return module.Result{State: module.JobSucceeded, Data: data}, nil
}

// writeEnv replaces a stack's project .env (see writeEnvAt). It never logs the content.
func (m *Module) writeEnv(ctx context.Context, p actionParams, emit func(module.Event)) (module.Result, error) {
	emit(module.Event{Kind: module.EventState, State: module.JobRunning})
	files, err := m.composeFiles(ctx, p.Stack)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	validate := m.validateEnv
	if validate == nil {
		validate = validateComposeEnv
	}
	logf := func(s string) { emit(module.Event{Kind: module.EventLog, Message: s}) }
	rep, err := writeEnvAt(ctx, filepath.Dir(files[0]), files, []byte(p.Content), p.BaseSHA256, validate, logf)
	if err != nil {
		return module.Result{State: module.JobFailed, Error: err.Error()}, nil
	}
	emit(module.Event{Kind: module.EventState, State: module.JobSucceeded})
	data, _ := json.Marshal(map[string]any{"sha256": rep.SHA256, "created": rep.Created, "target": rep.Target})
	return module.Result{State: module.JobSucceeded, Data: data}, nil
}
