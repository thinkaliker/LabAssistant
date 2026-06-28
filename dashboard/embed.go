// Package dashboard embeds the static web assets (HTML, vendored Alpine.js + Bulma)
// served by the manager. The dashboard is a pure client of the manager's REST + SSE API.
package dashboard

import "embed"

// Assets holds the embedded dashboard files, served at the manager's web root.
//
//go:embed index.html app.js vendor
var Assets embed.FS
