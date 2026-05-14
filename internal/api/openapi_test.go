package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestOpenAPI_DocumentIsValid loads the embedded spec through
// kin-openapi's full validator. A failure here means the YAML is
// malformed, references a nonexistent schema, declares an unknown
// type, etc. — the kind of thing that silently breaks MCP adapters
// and code generators downstream.
func TestOpenAPI_DocumentIsValid(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(openapiYAML)
	if err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("openapi validate: %v", err)
	}
	if doc.Info == nil || doc.Info.Title == "" {
		t.Fatal("info.title required")
	}
	if doc.Info.Version == "" {
		t.Fatal("info.version required")
	}
}

// TestOpenAPI_OperationIDsUniqueAndSet asserts every operation in the
// spec has an operationId, and the set is unique across the document.
// This is the single most important property for MCP: an OpenAPI→MCP
// bridge maps each operationId to a tool name, so a missing or
// duplicate id collapses tools silently.
func TestOpenAPI_OperationIDsUniqueAndSet(t *testing.T) {
	doc := mustLoadOpenAPI(t)
	seen := map[string]string{} // opID → "METHOD path"
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID == "" {
				t.Errorf("%s %s: missing operationId", method, path)
				continue
			}
			r := method + " " + path
			if other, dup := seen[op.OperationID]; dup {
				t.Errorf("duplicate operationId %q (used by %s and %s)",
					op.OperationID, other, r)
				continue
			}
			seen[op.OperationID] = r
		}
	}
}

// TestOpenAPI_EveryOperationHasMCPMetadata is a quality bar. An
// MCP-friendly spec describes every operation in prose dense enough
// that an agent can pick the right tool without reading the
// implementation. Require: summary + tags on every op.
func TestOpenAPI_EveryOperationHasMCPMetadata(t *testing.T) {
	doc := mustLoadOpenAPI(t)
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			r := method + " " + path
			if op.Summary == "" {
				t.Errorf("%s: missing summary", r)
			}
			if len(op.Tags) == 0 {
				t.Errorf("%s: missing tags", r)
			}
		}
	}
}

// TestOpenAPI_DocumentedRoutesMatchMountedRoutes is the drift check
// between the spec and the route table that Mount() consumes. Every
// gated route must appear in the spec; every spec path must be in
// the route table (modulo the always-open routes we whitelist below).
func TestOpenAPI_DocumentedRoutesMatchMountedRoutes(t *testing.T) {
	mounted := map[string]bool{}
	for _, r := range gatedRoutes() {
		mounted[r.method+" "+r.path] = true
	}
	for _, r := range authRoutes() {
		mounted[r.method+" "+r.path] = true
	}
	// Always-open routes — registered directly in Mount, not via the
	// route table. The spec documents them too so MCP discovery
	// works without a token; reflect them here.
	for _, r := range []string{
		"GET /healthz",
		"GET /openapi.yaml",
		"GET /openapi.json",
		"GET /swagger.json",
		"GET /docs",
		"GET /swagger",
		"GET /v1/auth-status",
	} {
		mounted[r] = true
	}

	doc := mustLoadOpenAPI(t)
	documented := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented[method+" "+path] = true
		}
	}

	var missingFromSpec, missingFromMux []string
	for r := range mounted {
		if !documented[r] {
			missingFromSpec = append(missingFromSpec, r)
		}
	}
	for r := range documented {
		if !mounted[r] {
			missingFromMux = append(missingFromMux, r)
		}
	}

	if len(missingFromSpec) > 0 || len(missingFromMux) > 0 {
		sort.Strings(missingFromSpec)
		sort.Strings(missingFromMux)
		t.Errorf("openapi ↔ Mount drift:\n  mounted but undocumented: %v\n  documented but not mounted: %v",
			missingFromSpec, missingFromMux)
	}
}

// TestOpenAPI_EveryHandlerIDHasAHandler closes the second drift loop:
// every handlerID in the route tables must resolve to a real method on
// *handlers. The handlerFor switch panics on miss, so this test just
// touches every id to surface the panic during `go test`.
func TestOpenAPI_EveryHandlerIDHasAHandler(t *testing.T) {
	h := &handlers{}
	for _, r := range append(gatedRoutes(), authRoutes()...) {
		func(id string) {
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("handlerFor(%q) panicked: %v", id, rec)
				}
			}()
			if h.handlerFor(id) == nil {
				t.Errorf("handlerFor(%q) returned nil", id)
			}
		}(r.handlerID)
	}
}

// TestOpenAPIYAMLHandler_ServesEmbeddedSpec confirms the served bytes
// match the embed exactly. An MCP adapter would pin to the served
// version; we don't want runtime transformation surprising it.
func TestOpenAPIYAMLHandler_ServesEmbeddedSpec(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(openapiYAMLHandler))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/openapi.yaml")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Type"), "application/yaml"; !strings.HasPrefix(got, want) {
		t.Errorf("Content-Type: got %q, want prefix %q", got, want)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != string(openapiYAML) {
		t.Errorf("served bytes differ from embedded bytes (got %d, want %d)", len(body), len(openapiYAML))
	}
}

// TestOpenAPIJSONHandler_ParsesAsJSON asserts the JSON-rendered
// version parses cleanly and contains the same top-level structure.
// Defense against an accidental yaml→json transformation regression
// (e.g. an upstream yaml.v3 bump that changes how int keys map).
func TestOpenAPIJSONHandler_ParsesAsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(openapiJSONHandler))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/openapi.json")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	for _, k := range []string{"openapi", "info", "paths", "components"} {
		if _, ok := got[k]; !ok {
			t.Errorf("served JSON missing top-level key %q", k)
		}
	}
}

// TestDocsHandler_ServesSwaggerUI confirms the /docs route returns
// an HTML page that references the canonical spec URL. We don't
// validate the rendered DOM — the spec drift test is the
// machine-readable contract — but we DO assert the loader points at
// /openapi.yaml, because that's the contract Try-It-Out relies on.
func TestDocsHandler_ServesSwaggerUI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(docsHTMLHandler))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/docs")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Type"), "text/html"; !strings.HasPrefix(got, want) {
		t.Errorf("Content-Type: got %q, want prefix %q", got, want)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "/openapi.yaml") {
		t.Errorf("page does not reference /openapi.yaml — Try-It-Out will not work against the right spec")
	}
	if !strings.Contains(string(body), "swagger-ui") {
		t.Errorf("page does not load the swagger-ui bundle")
	}
}

// mustLoadOpenAPI loads + validates the embedded spec or fails the
// test. Used by every other test in this file.
func mustLoadOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(openapiYAML)
	if err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc
}
