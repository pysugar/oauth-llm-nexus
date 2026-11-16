// Package ui serves the standalone Magpie-inspired Web console mounted at /ui.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed assets/*
var embeddedAssets embed.FS

// Handler returns an http.Handler serving the embedded /ui SPA and static assets.
func Handler() http.Handler {
	sub, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic("ui: embedded assets sub-fs failed: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trimmed := strings.TrimPrefix(r.URL.Path, "/ui")
		trimmed = strings.TrimPrefix(trimmed, "/")

		// Serve index.html for /ui, /ui/, or any client-side route without a file extension.
		if trimmed == "" || !strings.Contains(path.Base(trimmed), ".") {
			raw, err := fs.ReadFile(sub, "index.html")
			if err != nil {
				http.Error(w, "UI unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}

		// Ensure embedded static assets resolve relative to assets/ root.
		reqCopy := r.Clone(r.Context())
		reqCopy.URL.Path = "/" + trimmed
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, reqCopy)
	})
}
