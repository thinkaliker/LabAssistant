// Command manager is the LabAssistant control host: it serves the dashboard and REST
// API, hosts the associate mTLS stream, and owns the CA and persistent state.
//
// Subcommands:
//
//	manager serve            run the manager (default)
//	manager enroll           register a host and mint an enrollment bundle
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/thinkaliker/labassistant/internal/paths"
	"github.com/thinkaliker/labassistant/manager"
	"github.com/thinkaliker/labassistant/manager/config"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe(args)
	case "enroll":
		err = runEnroll(args)
	default:
		err = fmt.Errorf("unknown subcommand %q (want serve or enroll)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "manager:", err)
		os.Exit(1)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	home := fs.String("home", "", "base directory for config/, data/, logs/ (overrides $LABASSISTANT_HOME)")
	_ = fs.Parse(args)

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
	app, err := manager.NewApp(layout, cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Serve(ctx)
}

func runEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	home := fs.String("home", "", "base directory (overrides $LABASSISTANT_HOME)")
	name := fs.String("name", "", "human-readable host name")
	ip := fs.String("ip", "", "host IP or address")
	addr := fs.String("addr", "localhost:8443", "manager address the associate dials")
	serverName := fs.String("server-name", "localhost", "manager TLS server name (must match its cert)")
	out := fs.String("out", "associate-bundle.json", "output path for the enrollment bundle")
	_ = fs.Parse(args)

	if *name == "" {
		return fmt.Errorf("-name is required")
	}
	layout, err := paths.Resolve(*home)
	if err != nil {
		return err
	}
	if err := layout.EnsureDirs(); err != nil {
		return err
	}
	b, err := manager.Enroll(layout, *name, *ip, *addr, *serverName)
	if err != nil {
		return err
	}
	if err := b.Save(*out); err != nil {
		return err
	}
	fmt.Printf("enrolled host %q (id %s)\nbundle written to %s\n", *name, b.HostID, *out)
	return nil
}
