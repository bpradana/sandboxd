// Package web embeds the static web console served at /ui/.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"strings"
)

// Console serves one consistent asset version, including relative ES module
// imports, whenever the embedded HTML, JavaScript or styles change.
type Console struct {
	FS      fs.FS
	Version string
	Index   []byte
}

func LoadConsole() (*Console, error) {
	assets := FS()
	version, err := bundleVersion(assets)
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, err
	}
	html := string(index)
	for _, name := range []string{"app.js", "style.css", "favicon.svg", "vendor/xterm.js", "vendor/addon-fit.js", "vendor/xterm.css"} {
		html = strings.ReplaceAll(html, `"`+name+`"`, `"assets/`+version+`/`+name+`"`)
	}
	return &Console{FS: assets, Version: version, Index: []byte(html)}, nil
}

func bundleVersion(assets fs.FS) (string, error) {
	hash := sha256.New()
	err := fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		_, _ = io.WriteString(hash, name+"\x00")
		file, err := assets.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

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
