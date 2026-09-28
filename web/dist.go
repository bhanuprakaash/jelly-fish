// Package web embeds the built PWA served by the api role.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded PWA build, rooted at its files.
func FS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
