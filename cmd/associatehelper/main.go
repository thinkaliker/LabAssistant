// Command associatehelper is the small privileged helper the associate invokes to run
// elevated actions (e.g. package updates, reboots). It is the only component that runs
// with elevated privileges, keeping the associate itself unprivileged.
package main

import (
	"fmt"
	"log/slog"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "associatehelper:", err)
		os.Exit(1)
	}
}

func run() error {
	slog.Info("associatehelper invoked")

	// TODO(slice-2): read an elevated action request on stdin, execute it, stream
	// results back to the associate. In slice 1 the associate runs as root in dev and
	// this helper is a placeholder. See BUILD.md.
	return nil
}
