// Package ui serves the bundled React SPA. The web app's build output is
// expected at internal/ui/dist via "make web". When the dist directory is
// empty (development without a built UI), Handler() falls back to a small
// HTML stub that links to the API.
package ui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns the http.Handler for the bundled UI.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return fallback()
	}
	return handlerFromFS(sub)
}

// handlerFromFS is the body of Handler() factored out so tests can
// inject an in-memory filesystem; production callers always go through
// Handler() with the embedded distFS.
func handlerFromFS(sub fs.FS) http.Handler {
	indexBytes, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return fallback()
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve real asset files from /assets/* (and any other built
		// resource that exists on disk) via the file server. SPA routes
		// (anything else, including "/") fall back to index.html, which
		// we write directly to avoid http.FileServer's automatic
		// /index.html → / redirect.
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" || clean == "index.html" {
			serveIndex(w, r, indexBytes)
			return
		}
		if _, err := fs.Stat(sub, clean); err != nil {
			serveIndex(w, r, indexBytes)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(body))
}

func fallback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(stubHTML))
	})
}

const stubHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>signalwatch</title>
<style>body{font-family:system-ui,-apple-system,sans-serif;max-width:640px;margin:60px auto;color:#222;padding:0 20px;line-height:1.5}code{background:#f4f4f5;padding:2px 6px;border-radius:4px}h1{margin-bottom:8px}</style>
</head><body>
<h1>signalwatch</h1>
<p>The bundled UI hasn't been built yet. Run <code>make web</code> at the repo root to build it, or use the API directly.</p>
<ul>
<li><code>GET  /v1/rules</code></li>
<li><code>POST /v1/rules</code></li>
<li><code>GET  /v1/subscribers</code></li>
<li><code>POST /v1/subscribers</code></li>
<li><code>GET  /v1/subscriptions</code></li>
<li><code>POST /v1/subscriptions</code></li>
<li><code>GET  /v1/incidents</code></li>
<li><code>GET  /v1/notifications</code></li>
<li><code>GET  /v1/states</code></li>
<li><code>POST /v1/events</code></li>
</ul>
</body></html>`
