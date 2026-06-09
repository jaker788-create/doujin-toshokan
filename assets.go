package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"doujin/internal/archive"
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
		// A "<archive>.cbz/<entry>" virtual path is served by streaming the entry
		// out of the zip; the guard runs against the real archive file, not the
		// (non-existent) virtual path.
		if archivePath, entry, ok := archive.SplitArchivePath(p); ok {
			if !paths.IsWithinRoots(archivePath, a.roots()) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			rc, err := archive.OpenEntry(archivePath, entry)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				http.Error(w, "read error", http.StatusInternalServerError)
				return
			}
			modTime := time.Time{}
			if st, err := os.Stat(archivePath); err == nil {
				modTime = st.ModTime()
			}
			// ServeContent sets Content-Type from the entry name and supports Range.
			http.ServeContent(w, r, entry, modTime, bytes.NewReader(data))
			return
		}
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
		// Archive virtual path: thumbnail the entry decoded from inside the zip.
		if archivePath, entry, ok := archive.SplitArchivePath(p); ok {
			if !paths.IsWithinRoots(archivePath, a.roots()) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			out, err := thumbs.GetThumbnailArchive(archivePath, entry, width, config.ThumbCacheDir(a.dataDir))
			if err != nil {
				http.Error(w, "thumbnail error", http.StatusInternalServerError)
				return
			}
			http.ServeFile(w, r, out)
			return
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
