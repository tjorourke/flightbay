// Package docs embeds the documentation so flightbay-desk can serve it at
// /docs with no network access. The same Markdown builds the Hugo site in
// docs/site (hugo --source docs/site).
package docs

import "embed"

// FS holds site/content (the pages) and images (the screenshots).
//
//go:embed all:site/content images
var FS embed.FS
