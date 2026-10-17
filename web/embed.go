// Package web embeds the built single-page app (web/dist) into the binary.
// Run `make web` (npm run build) before `go build` to include a fresh UI;
// without it the server explains that the UI has not been built.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built app. ok is false when only the placeholder exists.
func Assets() (fsys fs.FS, ok bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return sub, false
	}
	return sub, true
}
