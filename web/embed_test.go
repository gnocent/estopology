package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	h := Handler()
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	for _, p := range []string{"/", "/c/clu-a", "/c/clu-a/node/x?at=1"} {
		rec := get(p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<div id="app">`) || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Header().Get("Cache-Control"))
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("%s: no CSP", p)
		}
	}
	// A hashed asset referenced by index.html is served with a long cache.
	body := get("/").Body.String()
	i := strings.Index(body, "/assets/")
	if i < 0 {
		t.Fatal("index.html references no asset")
	}
	asset := body[i : i+strings.IndexByte(body[i:], '"')]
	rec := get(asset)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("%s: %d %q", asset, rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := get("/api/v1/nope"); rec.Code != 404 {
		t.Errorf("API path served by the UI: %d", rec.Code)
	}
}
