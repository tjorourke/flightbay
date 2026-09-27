// Package web embeds the Flightbay desk UI.
package web

import "embed"

// FS holds index.html, app.js, app.css and vendored xterm.js.
//
//go:embed index.html app.js app.css vendor
var FS embed.FS
