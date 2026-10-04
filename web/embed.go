// Package web embeds the compiled web UI (web/dist, built with `npm run
// build` and committed) so that the estopo binary serves it without any
// file or network access (SPEC §4, §8.1).
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the UI: hashed assets with a long cache, index.html for
// every other application route (permalinks), never for /api or /healthz.
func Handler() http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed directive guarantees the directory
	}
	files := http.FileServer(http.FS(root))
	index, _ := fs.ReadFile(root, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		if strings.HasPrefix(p, "/api/") || p == "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		name := strings.TrimPrefix(p, "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(root, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}
