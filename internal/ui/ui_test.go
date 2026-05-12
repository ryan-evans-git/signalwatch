package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Handler() returns the package-level handler. In tests the embedded
// dist/ is empty (just a .gitkeep) so this exercises the fallback
// path — handlerFromFS errors on missing index.html → fallback().
func TestHandler_FallbackWhenDistEmpty(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	t.Run("root serves stub HTML", func(t *testing.T) {
		r := get(t, srv, "/")
		if r.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", r.StatusCode)
		}
		if !strings.Contains(r.Body, "signalwatch") || !strings.Contains(r.Body, "make web") {
			t.Errorf("stub HTML missing expected content: %s", r.Body[:200])
		}
	})

	t.Run("non-root paths 404 in fallback", func(t *testing.T) {
		r := get(t, srv, "/nonexistent")
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("status: want 404, got %d", r.StatusCode)
		}
	})
}

// handlerFromFS with a populated MapFS exercises the main code path:
// serving index.html on SPA routes and assets via http.FileServer.
func TestHandlerFromFS_PopulatedDist(t *testing.T) {
	mapFS := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte("<!doctype html><html><body><div id=root></div></body></html>"),
		},
		"assets/app.css": &fstest.MapFile{
			Data: []byte("body { color: red; }"),
		},
	}
	h := handlerFromFS(mapFS)
	srv := httptest.NewServer(h)
	defer srv.Close()

	t.Run("root serves index.html", func(t *testing.T) {
		r := get(t, srv, "/")
		if r.StatusCode != http.StatusOK || !strings.Contains(r.Body, "id=root") {
			t.Fatalf("status=%d body=%s", r.StatusCode, r.Body)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type: want text/html prefix, got %q", ct)
		}
		if cc := r.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("Cache-Control: want no-cache, got %q", cc)
		}
	})

	t.Run("/index.html also serves index", func(t *testing.T) {
		r := get(t, srv, "/index.html")
		if r.StatusCode != http.StatusOK || !strings.Contains(r.Body, "id=root") {
			t.Fatalf("status=%d body=%q", r.StatusCode, r.Body)
		}
	})

	t.Run("asset file served verbatim", func(t *testing.T) {
		r := get(t, srv, "/assets/app.css")
		if r.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", r.StatusCode)
		}
		if !strings.Contains(r.Body, "color: red") {
			t.Errorf("asset body: %q", r.Body)
		}
	})

	t.Run("unknown route falls back to index", func(t *testing.T) {
		r := get(t, srv, "/some/spa/route")
		if r.StatusCode != http.StatusOK || !strings.Contains(r.Body, "id=root") {
			t.Fatalf("SPA fallback: status=%d body=%q", r.StatusCode, r.Body)
		}
	})
}

// httpResult is the (status, headers, body) tuple the helper returns.
// We deliberately don't surface *http.Response so the bodyclose linter
// doesn't flag callers that never call .Body.Close (the helper does).
type httpResult struct {
	StatusCode int
	Header     http.Header
	Body       string
}

// get fetches path and returns the response triple.
func get(t *testing.T, srv *httptest.Server, path string) httpResult {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return httpResult{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: string(body)}
}
