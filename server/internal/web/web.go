// Package web serves the built PWA out of the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist holds the Vite build output. `make build` (or scripts/quickstart.sh)
// writes apps/web's output here before compiling the binary.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the PWA, falling back to index.html so client-side routes
// survive a refresh or a bookmark opened from the iOS home screen.
func Handler() http.Handler {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: embedded dist missing: " + err.Error())
	}
	files := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(assets, name); err != nil {
			// Unknown path: hand it to the SPA router.
			serveIndex(w, r, assets)
			return
		}
		// Vite fingerprints filenames under /assets, so they can be cached hard.
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "web UI not built", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(index)
}
