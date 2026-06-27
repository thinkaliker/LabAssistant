// Command associatehelper is the small privileged helper the associate invokes to run
// elevated actions. It reads one elevated.Request on stdin, executes it against the same
// compiled-in modules, and streams elevated.Frames on stdout. It is the only component
// meant to run with elevated privileges.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/thinkaliker/labassistant/internal/elevated"
	"github.com/thinkaliker/labassistant/module"
	"github.com/thinkaliker/labassistant/modules"
)

func main() {
	byName := map[string]module.Module{}
	for _, m := range modules.Default() {
		byName[m.Manifest().Name] = m
	}
	if err := elevated.Serve(context.Background(), os.Stdin, os.Stdout, byName); err != nil {
		fmt.Fprintln(os.Stderr, "associatehelper:", err)
		os.Exit(1)
	}
}
