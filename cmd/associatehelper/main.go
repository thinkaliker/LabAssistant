// Command associatehelper is the small privileged helper the associate invokes to run
// elevated actions. It reads one elevated.Request (from --request, or stdin), executes it
// against the same compiled-in modules, and streams elevated.Frames on stdout. It is the only
// component meant to run with elevated privileges.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/thinkaliker/labassistant/internal/elevated"
	"github.com/thinkaliker/labassistant/module"
	"github.com/thinkaliker/labassistant/modules"
)

func main() {
	// The request is read from --request (a file) rather than stdin: under sudo's use_pty,
	// stdin is a pty shared with the password line and cannot carry the request cleanly.
	reqPath := flag.String("request", "", "path to a file holding the elevated request; default reads stdin")
	flag.Parse()

	byName := map[string]module.Module{}
	for _, m := range modules.Default() {
		byName[m.Manifest().Name] = m
	}

	var in io.Reader = os.Stdin
	if *reqPath != "" {
		f, err := os.Open(*reqPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "associatehelper:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}

	if err := elevated.Serve(context.Background(), in, os.Stdout, byName); err != nil {
		fmt.Fprintln(os.Stderr, "associatehelper:", err)
		os.Exit(1)
	}
}
