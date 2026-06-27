// Package elevated implements the IPC between the unprivileged associate and its single
// privileged helper. For an action that declares PrivilegeElevated, the associate spawns
// the helper (e.g. via sudo), writes a Request on stdin, and reads a stream of Frames on
// stdout. The helper runs the same compiled-in module and streams events back.
//
// This keeps the associate unprivileged while isolating elevated execution in one place.
package elevated

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/thinkaliker/labassistant/module"
)

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
// terminal result. command is a prefix such as ["sudo","/usr/local/bin/associatehelper"].
func Run(ctx context.Context, command []string, req Request, emit func(module.Event)) (module.Result, error) {
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
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return module.Result{}, err
	}

	enc := json.NewEncoder(stdin)
	if err := enc.Encode(req); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return module.Result{}, err
	}
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
		return module.Result{}, fmt.Errorf("helper exited: %w", err)
	}
	if !haveResult {
		return module.Result{}, errors.New("helper produced no result")
	}
	return result, nil
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
