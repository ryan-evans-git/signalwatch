package api

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"gopkg.in/yaml.v3"
)

// openapiYAML is the canonical, version-controlled OpenAPI spec for the
// signalwatch HTTP API. It is embedded into the binary so the running
// server can serve exactly the schema it was built with — no separate
// "did the doc get out of sync with this build?" anxiety. The
// internal/api/openapi_test.go drift check makes the same guarantee
// against the mounted routes.
//
//go:embed openapi.yaml
var openapiYAML []byte

// openapiYAMLHandler serves the spec verbatim with application/yaml.
// The route is intentionally unauthenticated (mounted outside the
// gated set in Mount) so MCP adapters, codegen tools, and curl-curious
// humans can discover the schema without holding a credential — same
// rationale as /healthz.
func openapiYAMLHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	// One day of CDN cacheability is fine — the spec changes with
	// releases, not within them. Clients holding a stale copy still
	// connect to the same endpoints; correctness is unaffected.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(openapiYAML)
}

// openapiJSONHandler converts the embedded YAML to JSON on the fly.
// We don't precompute the JSON at build time because the YAML is the
// source of truth and a separate JSON file would be one more thing to
// keep in sync. The conversion is cheap (a few hundred KB at most) and
// the route's response is cacheable.
//
// Errors here only fire on programmer mistakes in the YAML — a build
// that ships a malformed openapi.yaml would also fail the parity test
// in CI, so by the time this code runs in production the conversion
// is guaranteed to succeed. We still surface a 500 so any drift after
// shipping is debuggable rather than silent.
func openapiJSONHandler(w http.ResponseWriter, _ *http.Request) {
	var doc any
	if err := yaml.Unmarshal(openapiYAML, &doc); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// yaml.v3 decodes maps as map[string]any when keys are strings,
	// so the result round-trips through encoding/json cleanly. No
	// extra normalization needed.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		// Headers may already be flushed; we can't change the status.
		// Best we can do is record the failure for the operator.
		// (No retry helps the client at this point.)
		_ = err
	}
}
