// Package web embeds the Flightbay desk UI.
package web

import "embed"

// FS holds the desk (index.html, app.js, review.js), the start page
// (start.html, start.js), app.css and vendored xterm.js.
//
//go:embed index.html app.js review.js traffic.js ambient.js ambient-configure.js configure-clusters.js resilience.js ambient-map.js ambient-map-routing.js http-test.js http-mesh-test.js http-mesh-test.css global-publication.js request-path.js request-path-map.js request-path.css request-path-transport.css bookinfo-demo.js app.css ambient.css rbac.js resources-map.js start.html start.js vendor favicon.svg favicon-32.png apple-touch-icon.png
var FS embed.FS
