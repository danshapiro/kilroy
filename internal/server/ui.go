// Serves the embedded dashboard SPA at /ui/ with hash-routed deep links.
// Static assets are compiled into the binary via //go:embed.
package server

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed ui/index.html ui/viz.js ui/viz-render.js
var uiFS embed.FS

func uiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/ui")
		if path == "" {
			http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			return
		}
		if path == "/" || path == "/index.html" {
			serveUIFile(w, "ui/index.html", "text/html; charset=utf-8")
			return
		}
		switch path {
		case "/viz.js":
			serveUIFile(w, "ui/viz.js", "application/javascript; charset=utf-8")
		case "/viz-render.js":
			serveUIFile(w, "ui/viz-render.js", "application/javascript; charset=utf-8")
		default:
			http.NotFound(w, r)
		}
	})
}

func serveUIFile(w http.ResponseWriter, name, contentType string) {
	data, err := uiFS.ReadFile(name)
	if err != nil {
		http.Error(w, "ui asset not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(data)
}
