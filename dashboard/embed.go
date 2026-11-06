// Package dashboard embeds the static web assets (HTML, vendored Alpine.js + Basecoat CSS)
// served by the manager. The dashboard is a pure client of the manager's REST + SSE API.
package dashboard

import (
	"embed"
	"mime"
)

// index.html is generated from partials/ by gen.go; run `go generate ./dashboard` after editing
// a partial. The generated index.html is committed so this embed and a plain `go build` work
// without a mandatory generate step.
//
//go:generate go run gen.go

// Assets holds the embedded dashboard files, served at the manager's web root. The app component
// lives under js/ (ES modules merged by js/main.js).
//
// manifest.webmanifest, sw.js and icons/ make the dashboard installable as an app (PWA). The
// icons are PNG renderings of the flask in favicon.svg, since browsers want raster icons to
// install an app: icon-*.png on transparent, maskable-512.png (inside the central 80% circle
// launchers may crop to) and apple-touch-icon.png (iOS shows transparency as black) on white.
//
//go:embed index.html js favicon.svg stylesheet.css vendor manifest.webmanifest sw.js icons
var Assets embed.FS

// Go's built-in MIME table has no entry for web app manifests, and the system's may not either,
// so the file server would otherwise send it as text/plain.
func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}
