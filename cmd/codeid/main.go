// Command codeid prints the source fingerprint of the associate's import graph.
//
// scripts/manage.sh stamps the result into the associate and manager binaries at link time, so
// both sides can tell whether a manager update actually changed associate code. See
// internal/codefp for why a git revision will not do.
//
// Usage: go run ./cmd/codeid [package...]
//
// The default set is what an upgrade actually ships: the associate and its privileged helper.
// The helper is not reachable from the associate's own import graph, and elevated actions run
// inside it, so leaving it out would let a helper-only fix ship without any host being told it
// needed one.
package main

import (
	"fmt"
	"os"

	"github.com/thinkaliker/labassistant/internal/codefp"
)

func main() {
	targets := []string{"./cmd/associate", "./cmd/associatehelper"}
	if len(os.Args) > 1 {
		targets = os.Args[1:]
	}
	fp, err := codefp.Compute(".", targets...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeid:", err)
		os.Exit(1)
	}
	fmt.Println(fp)
}
