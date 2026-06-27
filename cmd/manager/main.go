// Command manager is the LabAssistant control host: it serves the dashboard and REST
// API, hosts the associate mTLS stream, and owns the CA and persistent state.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/thinkaliker/labassistant/internal/paths"
	"github.com/thinkaliker/labassistant/manager/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "manager:", err)
		os.Exit(1)
	}
}

func run() error {
	home := flag.String("home", "", "base directory for config/, data/, and logs/ (overrides $LABASSISTANT_HOME)")
	flag.Parse()

	layout, err := paths.Resolve(*home)
	if err != nil {
		return err
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}

	cfg, err := config.Load(layout.ConfigFile())
	if err != nil {
		return err
	}

	slog.Info("manager starting",
		"home", layout.Base,
		"http_addr", cfg.HTTPAddr,
		"grpc_addr", cfg.GRPCAddr,
	)

	// TODO(slice-1): generate CA, start gRPC ManagerService over mTLS, start REST API +
	// embedded dashboard. See BUILD.md.
	return nil
}
