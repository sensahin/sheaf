package api

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
)

func init() {
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// The web queue: a small static page that talks to the same /v1 API with a
// paired device's token. It has no build step and loads nothing from the
// network.
//
//go:embed web
var webFiles embed.FS

func (s *Server) mountWeb() {
	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	s.mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
}
