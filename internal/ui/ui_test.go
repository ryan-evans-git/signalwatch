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
		resp, body := get(t, srv, "/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		if !strings.Contains(body, "signalwatch") || !strings.Contains(body, "make web") {
			t.Errorf("stub HTML missing expected content: %s", body[:200])
		}
	})

	t.Run("non-root paths 404 in fallback", func(t *testing.T) {
		resp, _ := get(t, srv, "/nonexistent")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status: want 404, got %d", resp.StatusCode)
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
		resp, body := get(t, srv, "/")
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "id=root") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type: want text/html prefix, got %q", ct)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("Cache-Control: want no-cache, got %q", cc)
		}
	})

	t.Run("/index.html also serves index", func(t *testing.T) {
		resp, body := get(t, srv, "/index.html")
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "id=root") {
			t.Fatalf("status=%d body=%q", resp.StatusCode, body)
		}
	})

	t.Run("asset file served verbatim", func(t *testing.T) {
		resp, body := get(t, srv, "/assets/app.css")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		if !strings.Contains(body, "color: red") {
			t.Errorf("asset body: %q", body)
		}
	})

	t.Run("unknown route falls back to index", func(t *testing.T) {
		resp, body := get(t, srv, "/some/spa/route")
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "id=root") {
			t.Fatalf("SPA fallback: status=%d body=%q", resp.StatusCode, body)
		}
	})
}

// Helper.
func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}
