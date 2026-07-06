// Package dashboard embeds the static web assets (HTML, vendored Alpine.js + Bulma)
// served by the manager. The dashboard is a pure client of the manager's REST + SSE API.
package dashboard

import "embed"

// index.html is generated from partials/ by gen.go; run `go generate ./dashboard` after editing
// a partial. The generated index.html is committed so this embed and a plain `go build` work
// without a mandatory generate step.
//
//go:generate go run gen.go

// Assets holds the embedded dashboard files, served at the manager's web root. The app component
// lives under js/ (ES modules merged by js/main.js).
//
//go:embed index.html js favicon.svg stylesheet.css vendor
var Assets embed.FS
