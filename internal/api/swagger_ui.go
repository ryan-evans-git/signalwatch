package api

import (
	_ "embed"
	"net/http"
)

// swaggerUIHTML is the self-contained landing page for human API
// exploration. It boots a pinned Swagger UI bundle from a public CDN
// against the in-binary OpenAPI spec at /openapi.yaml. The bundle is
// referenced with Subresource-Integrity hashes so a CDN compromise
// can't silently swap the loader for something hostile.
//
// Operators running fully air-gapped should vendor the swagger-ui-dist
// JS/CSS into their own static bundle and override this handler in
// their host program — the rest of the API works without /docs.
//
//go:embed swagger_ui.html
var swaggerUIHTML []byte

// docsHTMLHandler serves the embedded Swagger UI page. Unauthenticated
// like the other discovery routes (the spec it loads is also
// unauthenticated; gated routes still require a bearer token when the
// user clicks Try-It-Out).
//
// The route is intentionally stable: agents that look for a Swagger UI
// landing page tend to probe `/docs`, `/swagger`, and `/api-docs`. We
// answer at all three so an agent never has to guess.
func docsHTMLHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The HTML embeds version-pinned CDN URLs; the JSON spec it
	// fetches changes between releases. A short cache window keeps
	// the page fresh while still being CDN-friendly.
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(swaggerUIHTML)
}
