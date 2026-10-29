// Package web embeds the static web console served at /ui/.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// FS is the console's document root.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embedded directory is fixed at build time
	}
	return sub
}
