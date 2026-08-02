package codefp

import (
	"os"
	"path/filepath"
	"testing"
)

// The fingerprint has to answer one question correctly: does redeploying this binary change
// the code it runs? These tests pin the two halves of that — edits inside the import graph
// move it, edits outside it do not.

func TestComputeIgnoresSourceOutsideTheImportGraph(t *testing.T) {
	dir := newModule(t)
	before := compute(t, dir)

	// A file no package in the target's graph imports — the moral equivalent of a dashboard
	// asset or a manager-only change. Comparing commits would flag this; a fingerprint must not.
	write(t, dir, "unrelated/unrelated.go", "package unrelated\n\nconst X = 1\n")
	if after := compute(t, dir); after != before {
		t.Errorf("fingerprint moved on an unrelated package: %s -> %s", before, after)
	}
}

func TestComputeTracksImportedPackages(t *testing.T) {
	dir := newModule(t)
	before := compute(t, dir)

	write(t, dir, "lib/lib.go", "package lib\n\nfunc Greet() string { return \"goodbye\" }\n")
	after := compute(t, dir)
	if after == before {
		t.Fatalf("fingerprint did not move when an imported package changed (%s)", before)
	}

	// And it is a fingerprint of content, not of time: reverting the edit must restore the
	// old value, or a revert would still look like a change that needs deploying.
	write(t, dir, "lib/lib.go", "package lib\n\nfunc Greet() string { return \"hello\" }\n")
	if again := compute(t, dir); again != before {
		t.Errorf("fingerprint not stable across a revert: %s -> %s -> %s", before, after, again)
	}
}

func TestComputeTracksTheMainPackage(t *testing.T) {
	dir := newModule(t)
	before := compute(t, dir)

	write(t, dir, "cmd/app/main.go", "package main\n\nimport \"example.test/fp/lib\"\n\nfunc main() { _ = lib.Greet(); _ = 2 }\n")
	if after := compute(t, dir); after == before {
		t.Errorf("fingerprint did not move when the main package changed (%s)", before)
	}
}

func TestComputeUnknownTarget(t *testing.T) {
	if _, err := Compute(newModule(t), "./does/not/exist"); err == nil {
		t.Error("expected an error for a package pattern that matches nothing")
	}
	if _, err := Compute(newModule(t)); err == nil {
		t.Error("expected an error when no target is given")
	}
}

// An upgrade ships several binaries at once, so a change to a package only the second one
// imports still has to move the fingerprint — otherwise a helper-only fix ships silently and
// no host is ever told it needs the upgrade.
func TestComputeCoversEveryTarget(t *testing.T) {
	dir := newModule(t)
	write(t, dir, "helperlib/helperlib.go", "package helperlib\n\nconst Mode = 1\n")
	write(t, dir, "cmd/helper/main.go", "package main\n\nimport \"example.test/fp/helperlib\"\n\nfunc main() { _ = helperlib.Mode }\n")

	both := func() string {
		t.Helper()
		fp, err := Compute(dir, "./cmd/app", "./cmd/helper")
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		return fp
	}
	before := both()

	// helperlib is unreachable from ./cmd/app, so this is exactly the change a single-target
	// fingerprint would miss.
	if solo := compute(t, dir); solo == "" {
		t.Fatal("unexpected empty fingerprint")
	}
	appOnlyBefore := compute(t, dir)
	write(t, dir, "helperlib/helperlib.go", "package helperlib\n\nconst Mode = 2\n")

	if after := both(); after == before {
		t.Errorf("fingerprint did not move when a second target's dependency changed (%s)", before)
	}
	if appOnly := compute(t, dir); appOnly != appOnlyBefore {
		t.Errorf("single-target fingerprint moved on a package it does not import: %s -> %s", appOnlyBefore, appOnly)
	}
}

// newModule writes a tiny module whose main package imports lib but not unrelated.
func newModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.test/fp\n\ngo 1.26\n")
	write(t, dir, "lib/lib.go", "package lib\n\nfunc Greet() string { return \"hello\" }\n")
	write(t, dir, "cmd/app/main.go", "package main\n\nimport \"example.test/fp/lib\"\n\nfunc main() { _ = lib.Greet() }\n")
	return dir
}

func compute(t *testing.T, dir string) string {
	t.Helper()
	fp, err := Compute(dir, "./cmd/app")
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(fp) != fpLen {
		t.Fatalf("Compute returned %q, want %d characters", fp, fpLen)
	}
	return fp
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
