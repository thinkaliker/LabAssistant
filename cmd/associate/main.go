// Command associate is the per-host agent: it dials home to the manager over a
// persistent mTLS stream, advertises its modules, and runs actions on the host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/thinkaliker/labassistant/associate"
	"github.com/thinkaliker/labassistant/internal/bundle"
	"github.com/thinkaliker/labassistant/modules"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "associate:", err)
		os.Exit(1)
	}
}

func run() error {
	bundlePath := flag.String("bundle", "associate-bundle.json", "path to the enrollment bundle")
	helper := flag.String("helper", "", "path to the associatehelper binary for elevated actions")
	useSudo := flag.Bool("sudo", false, "invoke the helper via sudo")
	flag.Parse()

	b, err := bundle.Load(*bundlePath)
	if err != nil {
		return err
	}

	a := associate.New(b, modules.Default()...)
	a.SetBundlePath(*bundlePath)
	if *helper != "" {
		a.SetHelper(*helper, *useSudo)
	}
	slog.Info("associate starting", "host", b.HostID, "manager", b.ManagerAddr, "helper", *helper)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Run(ctx)
}
