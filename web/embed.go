// Package web embeds the built single-page UI so celld ships as one binary.
// Run `make web` (or `pnpm build` in this directory) before building celld;
// the committed dist/ keeps `go build` working without Node installed.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built assets rooted at dist/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // build-time guarantee: dist/ exists in the embed
	}
	return sub
}
