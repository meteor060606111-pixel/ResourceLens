package frontend

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var builtUI embed.FS

// Assets exposes the Vite output at its root to both Wails and the local API.
func Assets() fs.FS {
	assets, err := fs.Sub(builtUI, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
