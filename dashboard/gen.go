//go:build ignore

// Command gen assembles dashboard/index.html from the ordered fragments in dashboard/partials/.
// index.html is a generated file — edit the partials, then run `go generate ./dashboard` (or
// `go run gen.go` from this directory). The generated index.html is committed so a plain
// `go build` (and the //go:embed in embed.go) works without a mandatory generate step.
package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
)

// parts lists the partials in document order. It is an explicit list, not a glob, so fragment
// placement is controlled here rather than by filename sorting.
var parts = []string{
	"00-head-nav.html",
	"10-shell-banners.html",
	"20-page-overview.html",
	"21-page-hosts.html",
	"22-page-services.html",
	"23-page-updates.html",
	"24-page-scheduler.html",
	"25-page-audit.html",
	"26-page-settings.html",
	"30-shell-close.html",
	"40-page-login.html",
	"50-modals.html",
	"60-job-panel.html",
	"70-host-modals.html",
	"90-foot.html",
}

// note is injected right after the doctype (never before it, so the page stays in standards mode)
// to warn against editing the generated file by hand.
const (
	doctype = "<!DOCTYPE html>\n"
	note    = "<!-- Generated from dashboard/partials/ by gen.go. Do not edit by hand — edit the\n     partials, then run `go generate ./dashboard`. -->\n"
)

func main() {
	var buf bytes.Buffer
	for _, p := range parts {
		b, err := os.ReadFile(filepath.Join("partials", p))
		if err != nil {
			log.Fatalf("read partial %s: %v", p, err)
		}
		buf.Write(b)
	}
	out := buf.Bytes()
	if !bytes.HasPrefix(out, []byte(doctype)) {
		log.Fatalf("first partial must start with %q", doctype)
	}
	out = append([]byte(doctype+note), out[len(doctype):]...)
	if err := os.WriteFile("index.html", out, 0o644); err != nil {
		log.Fatalf("write index.html: %v", err)
	}
}
