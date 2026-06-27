// Command associate is the per-host agent: it dials home to the manager over a
// persistent mTLS stream, advertises its modules, and runs actions on the host.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "associate:", err)
		os.Exit(1)
	}
}

func run() error {
	bundle := flag.String("bundle", "", "path to the enrollment bundle (host id, manager address, certs)")
	flag.Parse()

	slog.Info("associate starting", "bundle", *bundle)

	// TODO(slice-1): load the enrollment bundle, dial the manager via gRPC over mTLS,
	// send Hello with module manifests + detection, run the heartbeat + command queue.
	// See BUILD.md.
	return nil
}
