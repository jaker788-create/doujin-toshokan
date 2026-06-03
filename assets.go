package main

import (
	"net/http"
	"os"
	"strconv"

	"doujin/internal/config"
	"doujin/internal/paths"
	"doujin/internal/thumbs"
)

// assetHandler serves the binary library files that <img> tags request by absolute
// path: /image (the original) and /thumb (a cached, resized JPEG). Both run the
// path-traversal guard before any filesystem access, exactly like the Python
// /image and /thumb routes. Wails invokes this handler for requests that the
// embedded frontend assets do not satisfy. It is intentionally unexported so Wails
// does not bind it as a frontend-callable method.
func (a *App) assetHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/image", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if !paths.IsWithinRoots(p, a.roots()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, p)
	})

	mux.HandleFunc("/thumb", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		width := 240
		if ws := r.URL.Query().Get("w"); ws != "" {
			if n, err := strconv.Atoi(ws); err == nil && n > 0 {
				width = n
			}
		}
		if !paths.IsWithinRoots(p, a.roots()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		out, err := thumbs.GetThumbnail(p, width, config.ThumbCacheDir(a.dataDir))
		if err != nil {
			http.Error(w, "thumbnail error", http.StatusInternalServerError)
			return
		}
		http.ServeFile(w, r, out)
	})

	return mux
}
