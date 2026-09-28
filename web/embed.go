// Package web embeds the Flightbay desk UI.
package web

import "embed"

// FS holds index.html, app.js, review.js, app.css and vendored xterm.js.
//
//go:embed index.html app.js review.js app.css vendor favicon.ico favicon.svg favicon-32.png apple-touch-icon.png
var FS embed.FS
