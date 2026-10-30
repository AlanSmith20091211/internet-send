// Package web embeds the browser UI so the hub ships as a single binary and
// works on networks with no internet access (e.g. a phone hotspot).
package web

import "embed"

//go:embed index.html app.js style.css
var FS embed.FS
