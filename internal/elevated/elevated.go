// Package elevated implements the IPC between the unprivileged associate and its single
// privileged helper. For an action that declares PrivilegeElevated, the associate spawns
// the helper (e.g. via sudo), writes a Request on stdin, and reads a stream of Frames on
// stdout. The helper runs the same compiled-in module and streams events back.
//
// This keeps the associate unprivileged while isolating elevated execution in one place.
package elevated

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/thinkaliker/labassistant/module"
)

// ErrSudoPassword indicates the helper could not be elevated because sudo needs a password:
// passwordless sudo is not configured, or the supplied password was wrong. The associate
// turns this into a JobNeedsSudoPassword result so the manager can prompt for one.
var ErrSudoPassword = errors.New("sudo password required")

// Request is what the associate sends the helper on stdin.
type Request struct {
	JobID  string          `json:"jobId"`
	Module string          `json:"module"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Frame is one line on the helper's stdout: either an Event or the terminal Result.
type Frame struct {
	Event  *module.Event  `json:"event,omitempty"`
	Result *module.Result `json:"result,omitempty"`
}

// Run spawns the helper command, sends req, forwards events to emit, and returns the
// terminal result. command is a prefix such as ["sudo","-n","/usr/local/bin/associatehelper"].
// When password is non-empty (command uses `sudo -S`) it is written as the first line of
// stdin for sudo to consume; the rest of stdin carries the request to the helper. If sudo
// cannot authenticate, Run returns ErrSudoPassword.
func Run(ctx context.Context, command []string, password string, req Request, emit func(module.Event)) (module.Result, error) {
	if len(command) == 0 {
		return module.Result{}, errors.New("no helper command configured")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return module.Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return module.Result{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return module.Result{}, err
	}

	// Writes are best-effort: a `sudo -n` that needs a password exits before reading stdin,
	// so these can fail with a broken pipe — the real cause surfaces from stderr below.
	if password != "" {
		_, _ = io.WriteString(stdin, password+"\n")
	}
	_ = json.NewEncoder(stdin).Encode(req)
	_ = stdin.Close()

	var result module.Result
	haveResult := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var f Frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			continue
		}
		switch {
		case f.Event != nil:
			emit(*f.Event)
		case f.Result != nil:
			result = *f.Result
			haveResult = true
		}
	}
	if err := cmd.Wait(); err != nil {
		if !haveResult && isSudoAuthFailure(stderr.String()) {
			return module.Result{}, ErrSudoPassword
		}
		return module.Result{}, fmt.Errorf("helper exited: %w", err)
	}
	if !haveResult {
		return module.Result{}, errors.New("helper produced no result")
	}
	return result, nil
}

// isSudoAuthFailure reports whether sudo's stderr indicates it could not authenticate
// (no passwordless rule, or a wrong/missing password) rather than the helper itself failing.
func isSudoAuthFailure(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, m := range []string{
		"a password is required",
		"a terminal is required",
		"incorrect password",
		"sorry, try again",
		"no askpass program",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// Serve runs the helper side: read one Request from in, execute it against registry,
// stream Frames to out. modulesByName maps module name to implementation.
func Serve(ctx context.Context, in io.Reader, out io.Writer, modulesByName map[string]module.Module) error {
	var req Request
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	enc := json.NewEncoder(out)

	m, ok := modulesByName[req.Module]
	if !ok {
		return enc.Encode(Frame{Result: &module.Result{State: module.JobFailed, Error: "unknown module: " + req.Module}})
	}
	emit := func(ev module.Event) { _ = enc.Encode(Frame{Event: &ev}) }
	res, err := m.Execute(ctx, module.ActionRequest{JobID: req.JobID, Action: req.Action, Params: req.Params}, emit)
	if err != nil {
		res = module.Result{State: module.JobFailed, Error: err.Error()}
	}
	return enc.Encode(Frame{Result: &res})
}
