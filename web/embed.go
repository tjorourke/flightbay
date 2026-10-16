// Package web embeds the Flightbay desk UI.
package web

import "embed"

// FS holds the desk (index.html, app.js, review.js), the start page
// (start.html, start.js), app.css and vendored xterm.js.
//
//go:embed index.html app.js review.js traffic.js app.css start.html start.js vendor favicon.svg favicon-32.png apple-touch-icon.png
var FS embed.FS
