// Package codefp computes a source fingerprint for a binary: a hash over every first-party
// package in its import graph.
//
// It exists because a git revision answers the wrong question. The manager stamps its build
// with the repo's commit, and every commit — a dashboard tweak, a README fix, a manager-only
// change — moves that commit. Comparing it against what a host's associate reports therefore
// marks every associate on every host as out of date after any commit at all, which makes the
// signal worthless and the upgrade a chore nobody does.
//
// A fingerprint over just the packages the associate actually compiles answers the question
// that matters: would redeploying this host change the code it runs? Only edits inside the
// associate's own import graph move it.
package codefp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
)

// fpLen is how much of the hash identifies a build, matching the short-commit length used
// elsewhere. Collisions at 48 bits do not matter here: a false "unchanged" needs an attacker,
// and nothing security-relevant hangs off this value.
const fpLen = 12

// pkg is the subset of `go list -json` output we hash over.
type pkg struct {
	ImportPath string
	Dir        string
	Module     *struct{ Path string }
	GoFiles    []string
	CgoFiles   []string
	EmbedFiles []string
}

// Compute returns the fingerprint of the first-party source reachable from targets (package
// patterns such as "./cmd/associate"), run with dir as the working directory. Dependencies
// outside the main module are excluded: they are pinned by go.mod, and a go.sum bump that
// changes nothing the associate compiles is not a reason to redeploy every host.
//
// Several targets fold into one fingerprint because an upgrade ships several binaries — the
// associate and its privileged helper go up together, so a change to either has to move the
// value that decides whether the upgrade happens.
func Compute(dir string, targets ...string) (string, error) {
	if len(targets) == 0 {
		return "", fmt.Errorf("no target packages given")
	}
	mainMod, err := mainModule(dir)
	if err != nil {
		return "", err
	}
	out, err := run(dir, "go", append([]string{"list", "-deps", "-json"}, targets...)...)
	if err != nil {
		return "", err
	}

	var pkgs []pkg
	seen := map[string]bool{} // targets share most of their graph; hash each package once
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return "", fmt.Errorf("parse go list output: %w", err)
		}
		// Standard library packages carry no Module at all; third-party ones carry a different
		// module path. Either way they are not ours.
		if p.Module == nil || p.Module.Path != mainMod || seen[p.ImportPath] {
			continue
		}
		seen[p.ImportPath] = true
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		return "", fmt.Errorf("no first-party packages found for %v (main module %s)", targets, mainMod)
	}
	return hashPackages(pkgs)
}

// hashPackages hashes package contents in a fixed order, so the fingerprint depends on the
// source and nothing else — not on `go list`'s ordering, not on where the checkout lives.
// File paths are folded to their base name for the same reason.
func hashPackages(pkgs []pkg) (string, error) {
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ImportPath < pkgs[j].ImportPath })
	h := sha256.New()
	for _, p := range pkgs {
		fmt.Fprintf(h, "pkg %s\n", p.ImportPath)
		files := append(append(append([]string{}, p.GoFiles...), p.CgoFiles...), p.EmbedFiles...)
		sort.Strings(files)
		for _, f := range files {
			sum, err := hashFile(p.Dir + string(os.PathSeparator) + f)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "file %s %s\n", f, sum)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:fpLen], nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func mainModule(dir string) (string, error) {
	out, err := run(dir, "go", "list", "-m")
	if err != nil {
		return "", err
	}
	mod := string(bytes.TrimSpace(out))
	if mod == "" {
		return "", fmt.Errorf("no main module in %s", dir)
	}
	return mod, nil
}

// run reports the tool's stderr on failure; "exit status 1" alone is useless for diagnosing a
// broken build tree.
func run(dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w: %s", name, args, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}
