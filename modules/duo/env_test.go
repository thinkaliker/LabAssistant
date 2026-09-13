package duo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thinkaliker/labassistant/module"
)

func okValidator(context.Context, []string, string) ([]byte, error) { return nil, nil }

func nopLog(string) {}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // WriteFile's mode is filtered by umask
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestReadEnvAt(t *testing.T) {
	t.Run("missing file is not an error", func(t *testing.T) {
		r, err := readEnvAt(t.TempDir(), maxEnvBytes)
		if err != nil || r.Exists || r.SHA256 != sha256Hex(nil) || len(r.Content) != 0 {
			t.Fatalf("got %+v, %v", r, err)
		}
	})
	t.Run("regular file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".env"), "A=1\n", 0o600)
		r, err := readEnvAt(dir, maxEnvBytes)
		if err != nil || !r.Exists || string(r.Content) != "A=1\n" || r.SHA256 != sha256Hex([]byte("A=1\n")) || r.OutsideDir {
			t.Fatalf("got %+v, %v", r, err)
		}
	})
	t.Run("truncates but hashes the whole file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".env"), "ABCDEFGH=1\n", 0o600)
		r, err := readEnvAt(dir, 4)
		if err != nil || !r.Truncated || string(r.Content) != "ABCD" || r.SHA256 != sha256Hex([]byte("ABCDEFGH=1\n")) {
			t.Fatalf("got %+v, %v", r, err)
		}
	})
	t.Run("symlink inside the stack directory", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "real.env"), "A=1\n", 0o600)
		if err := os.Symlink("real.env", filepath.Join(dir, ".env")); err != nil {
			t.Fatal(err)
		}
		r, err := readEnvAt(dir, maxEnvBytes)
		if err != nil || !r.Exists || r.OutsideDir || filepath.Base(r.Target) != "real.env" || string(r.Content) != "A=1\n" {
			t.Fatalf("got %+v, %v", r, err)
		}
	})
	t.Run("symlink outside the stack directory", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "stack")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "shared.env"), "S=1\n", 0o600)
		if err := os.Symlink("../shared.env", filepath.Join(dir, ".env")); err != nil {
			t.Fatal(err)
		}
		r, err := readEnvAt(dir, maxEnvBytes)
		if err != nil || !r.Exists || !r.OutsideDir || string(r.Content) != "S=1\n" {
			t.Fatalf("got %+v, %v", r, err)
		}
	})
	t.Run("dangling symlink is refused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink("nope.env", filepath.Join(dir, ".env")); err != nil {
			t.Fatal(err)
		}
		if _, err := readEnvAt(dir, maxEnvBytes); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("directory is refused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".env"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := readEnvAt(dir, maxEnvBytes); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("want not-a-regular-file error, got %v", err)
		}
	})
}

func TestWriteEnvAtExistingKeepsFileAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	writeFile(t, env, "A=1\n", 0o640)
	writeFile(t, env+".bak", "stale\n", 0o644) // an older, wider backup
	before, _ := os.Stat(env)

	rep, err := writeEnvAt(context.Background(), dir, []string{filepath.Join(dir, "compose.yaml")},
		[]byte("A=2\n"), sha256Hex([]byte("A=1\n")), okValidator, nopLog)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.BackedUp || rep.Created || rep.SHA256 != sha256Hex([]byte("A=2\n")) {
		t.Fatalf("report %+v", rep)
	}
	if got := readFile(t, env); got != "A=2\n" {
		t.Fatalf(".env = %q", got)
	}
	after, _ := os.Stat(env)
	if !os.SameFile(before, after) || perm(t, env) != 0o640 {
		t.Fatalf("file replaced or mode changed: same=%v mode=%v", os.SameFile(before, after), perm(t, env))
	}
	if got := readFile(t, env+".bak"); got != "A=1\n" {
		t.Fatalf(".env.bak = %q", got)
	}
	if p := perm(t, env+".bak"); p != 0o640 {
		t.Fatalf(".env.bak mode = %v, want 0640", p)
	}
}

func TestWriteEnvAtCreates0600(t *testing.T) {
	dir := t.TempDir()
	rep, err := writeEnvAt(context.Background(), dir, []string{filepath.Join(dir, "compose.yaml")},
		[]byte("A=1\n"), sha256Hex(nil), okValidator, nopLog)
	if err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(dir, ".env")
	if !rep.Created || rep.BackedUp || readFile(t, env) != "A=1\n" || perm(t, env) != 0o600 {
		t.Fatalf("report %+v, mode %v", rep, perm(t, env))
	}
	if _, err := os.Stat(env + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected .bak: %v", err)
	}
}

func TestWriteEnvAtValidationFailureTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	writeFile(t, env, "A=1\n", 0o600)
	content := "A=1\nPASSWORD=\"hunter2secret\n"
	fail := func(_ context.Context, _ []string, envFile string) ([]byte, error) {
		return []byte(fmt.Sprintf("failed to read %s: line 2: unterminated quoted value \"hunter2secret", envFile)), errors.New("exit status 1")
	}
	_, err := writeEnvAt(context.Background(), dir, []string{"compose.yaml"}, []byte(content), "", fail, nopLog)
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "hunter2secret") || !strings.Contains(msg, env) || strings.Contains(msg, "la-env-") {
		t.Fatalf("error not scrubbed / wrong path: %q", msg)
	}
	if readFile(t, env) != "A=1\n" {
		t.Fatal(".env was modified")
	}
	if _, err := os.Stat(env + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup written before validation: %v", err)
	}
}

func TestWriteEnvAtValidatorSeesCandidate(t *testing.T) {
	dir := t.TempDir()
	files := []string{"/srv/x/compose.yaml", "/srv/x/compose.override.yaml"}
	var gotFiles []string
	var gotContent string
	spy := func(_ context.Context, fs []string, envFile string) ([]byte, error) {
		gotFiles = fs
		b, err := os.ReadFile(envFile)
		gotContent = string(b)
		return nil, err
	}
	if _, err := writeEnvAt(context.Background(), dir, files, []byte("B=2\n"), "", spy, nopLog); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFiles, files) || gotContent != "B=2\n" {
		t.Fatalf("validator saw files=%v content=%q", gotFiles, gotContent)
	}
}

func TestWriteEnvAtFollowsSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stack")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "shared.env")
	writeFile(t, shared, "S=1\n", 0o600)
	link := filepath.Join(dir, ".env")
	if err := os.Symlink("../shared.env", link); err != nil {
		t.Fatal(err)
	}
	rep, err := writeEnvAt(context.Background(), dir, []string{"compose.yaml"}, []byte("S=2\n"), "", okValidator, nopLog)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, shared) != "S=2\n" || readFile(t, shared+".bak") != "S=1\n" || filepath.Base(rep.Target) != "shared.env" {
		t.Fatalf("target not written through the link: %+v", rep)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced")
	}
}

func TestWriteEnvAtRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".env"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := writeEnvAt(ctx, dir, []string{"c.yaml"}, []byte("A=1\n"), "", okValidator, nopLog); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("dangling symlink", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink("missing.env", filepath.Join(dir, ".env")); err != nil {
			t.Fatal(err)
		}
		if _, err := writeEnvAt(ctx, dir, []string{"c.yaml"}, []byte("A=1\n"), "", okValidator, nopLog); err == nil {
			t.Fatal("want error")
		}
		if _, err := os.Stat(filepath.Join(dir, "missing.env")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("dangling link target was created")
		}
	})
	t.Run("too large", func(t *testing.T) {
		big := []byte(strings.Repeat("A", maxEnvBytes+1))
		if _, err := writeEnvAt(ctx, t.TempDir(), []string{"c.yaml"}, big, "", okValidator, nopLog); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("changed since read", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".env"), "A=changed\n", 0o600)
		_, err := writeEnvAt(ctx, dir, []string{"c.yaml"}, []byte("A=1\n"), sha256Hex([]byte("A=1\n")), okValidator, nopLog)
		if !errors.Is(err, errEnvChanged) {
			t.Fatalf("got %v, want errEnvChanged", err)
		}
	})
	t.Run("created since read as missing", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".env"), "NEW=1\n", 0o600)
		_, err := writeEnvAt(ctx, dir, []string{"c.yaml"}, []byte("A=1\n"), sha256Hex(nil), okValidator, nopLog)
		if !errors.Is(err, errEnvChanged) {
			t.Fatalf("got %v, want errEnvChanged", err)
		}
	})
	t.Run("backup symlink is not followed", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		writeFile(t, victim, "precious\n", 0o600)
		writeFile(t, filepath.Join(dir, ".env"), "A=1\n", 0o600)
		if err := os.Symlink(victim, filepath.Join(dir, ".env.bak")); err != nil {
			t.Fatal(err)
		}
		if _, err := writeEnvAt(ctx, dir, []string{"c.yaml"}, []byte("A=2\n"), "", okValidator, nopLog); err == nil {
			t.Fatal("want error")
		}
		if readFile(t, victim) != "precious\n" || readFile(t, filepath.Join(dir, ".env")) != "A=1\n" {
			t.Fatal("write went through the .bak symlink")
		}
	})
}

func TestScrubEnvValues(t *testing.T) {
	content := "# comment with secretish words\nexport TOKEN=abcd1234\nPASS='quoted value'\nMULTI=\"line one\ncontinuation line\"\nSHORT=ab\n"
	in := `line 3: bad "quoted value' and abcd1234; continuation line; key SHORT is ab`
	got := scrubEnvValues(in, []byte(content))
	for _, leak := range []string{"abcd1234", "quoted value", "continuation line"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q in %q", leak, got)
		}
	}
	if !strings.Contains(got, "SHORT") {
		t.Errorf("key names should survive: %q", got)
	}
}

func TestManifestEnvActions(t *testing.T) {
	specs := map[string]module.ActionSpec{}
	for _, a := range New().Manifest().Actions {
		specs[a.Name] = a
	}
	r, ok := specs["read-env"]
	if !ok || r.Privilege != module.PrivilegeElevated || !r.ReadOnly {
		t.Errorf("read-env spec: %+v", r)
	}
	w, ok := specs["write-env"]
	// Destructive would route the params (the .env contents) through an approval into audit.log.
	if !ok || w.Privilege != module.PrivilegeElevated || w.Destructive || w.ReadOnly {
		t.Errorf("write-env spec: %+v", w)
	}
}

func TestExecuteSimulatedEnv(t *testing.T) {
	m := &Module{updates: map[string]imageUpdate{}} // useDocker false: simulated mode
	emit := func(module.Event) {}
	params, _ := json.Marshal(actionParams{Stack: "media"})
	res, err := m.Execute(context.Background(), module.ActionRequest{Action: "read-env", Params: params}, emit)
	if err != nil || res.State != module.JobSucceeded {
		t.Fatalf("read-env: %+v, %v", res, err)
	}
	var d struct {
		Content string `json:"content"`
		SHA256  string `json:"sha256"`
		Exists  bool   `json:"exists"`
	}
	if err := json.Unmarshal(res.Data, &d); err != nil || !d.Exists || d.SHA256 != sha256Hex([]byte(d.Content)) {
		t.Fatalf("read-env data %s: %v", res.Data, err)
	}
	params, _ = json.Marshal(actionParams{Stack: "media", Content: "A=1\n"})
	res, err = m.Execute(context.Background(), module.ActionRequest{Action: "write-env", Params: params}, emit)
	if err != nil || res.State != module.JobSucceeded {
		t.Fatalf("write-env: %+v, %v", res, err)
	}
}

func TestWriteActionsRejectUnparseableParams(t *testing.T) {
	m := &Module{useDocker: true, updates: map[string]imageUpdate{}}
	for _, action := range []string{"write-compose", "write-env"} {
		// A body the manager truncated at 1 MiB arrives as cut-off JSON.
		res, _ := m.executeDocker(context.Background(),
			module.ActionRequest{Action: action, Params: json.RawMessage(`{"stack":"x","content":"abc`)}, func(module.Event) {})
		if res.State != module.JobFailed || !strings.Contains(res.Error, "invalid params") {
			t.Errorf("%s: %+v", action, res)
		}
	}
}

func TestComposeUpArgs(t *testing.T) {
	cases := []struct {
		name  string
		multi bool
		p     actionParams
		want  []string
	}{
		{"plain", false, actionParams{}, []string{"compose", "-f", "/s/c.yaml", "up", "-d"}},
		{"orphans", false, actionParams{RemoveOrphans: true}, []string{"compose", "-f", "/s/c.yaml", "up", "-d", "--remove-orphans"}},
		{"orphans skipped for multi-file", true, actionParams{RemoveOrphans: true}, []string{"compose", "-f", "/s/c.yaml", "up", "-d"}},
		{"service", false, actionParams{RemoveOrphans: true, Service: "web"}, []string{"compose", "-f", "/s/c.yaml", "up", "-d", "--remove-orphans", "web"}},
	}
	for _, c := range cases {
		if got := composeUpArgs("/s/c.yaml", c.multi, c.p); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseConfigFiles(t *testing.T) {
	cases := map[string][]string{
		"":                             nil,
		"\n\n":                         nil,
		"/s/compose.yaml\n":            {"/s/compose.yaml"},
		"\n/s/a.yaml, /s/b.yaml\n/s/c": {"/s/a.yaml", "/s/b.yaml"},
	}
	for in, want := range cases {
		if got := parseConfigFiles(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseConfigFiles(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestValidateComposeEnvIntegration runs the real `docker compose config` (no daemon needed).
func TestValidateComposeEnvIntegration(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	writeFile(t, compose, "services:\n  app:\n    image: \"${IMG:?need IMG}\"\n", 0o644)
	// A broken project .env must not matter: --env-file replaces it.
	writeFile(t, filepath.Join(dir, ".env"), "BAD KEY=1\n", 0o600)
	check := func(content string) error {
		cand := filepath.Join(t.TempDir(), "cand.env")
		writeFile(t, cand, content, 0o600)
		_, err := validateComposeEnv(context.Background(), []string{compose}, cand)
		return err
	}
	if err := check("IMG=nginx\n"); err != nil {
		t.Errorf("valid candidate rejected: %v", err)
	}
	if err := check("IMG=\n"); err == nil {
		t.Error("missing required variable accepted")
	}
	if err := check("BAD KEY=1\nIMG=x\n"); err == nil {
		t.Error("key with a space accepted")
	}
}
