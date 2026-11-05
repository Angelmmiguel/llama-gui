package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// metaCSP is the policy SvelteKit writes into index.html in hash mode, cut
// down to the parts the server cares about.
const metaCSP = "default-src 'self'; script-src 'self' 'sha256-AAAA'"

// builtFS stands in for a compiled SvelteKit build.
var builtFS = fstest.MapFS{
	"index.html":              {Data: []byte("<!doctype html>shell")},
	"favicon.png":             {Data: []byte("icon")},
	"_app/immutable/x.js":     {Data: []byte("export const x = 1")},
	"assets/nested/style.css": {Data: []byte("body{}")},
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHandlerServesFilesAndFallsBackToIndex(t *testing.T) {
	h := handler(builtFS)

	for _, path := range []string{"/", "/messages/1", "/messages/1/attachments"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if body := rec.Body.String(); body != "<!doctype html>shell" {
			t.Errorf("GET %s body = %q, want the index", path, body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q, want no-store", path, got)
		}
	}

	rec := get(t, h, "/favicon.png")
	if rec.Code != http.StatusOK || rec.Body.String() != "icon" {
		t.Errorf("GET /favicon.png = %d %q, want the real file", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("GET /favicon.png Cache-Control = %q, want no-cache", got)
	}
}

func TestHandlerCachesHashedAssetsForever(t *testing.T) {
	rec := get(t, handler(builtFS), "/_app/immutable/x.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	const want = "public, max-age=31536000, immutable"
	if got := rec.Header().Get("Cache-Control"); got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
}

// The policy is authored in svelte.config.js and lands in index.html as a meta
// tag. The server hands it back as a header, which is the only place
// frame-ancestors can be expressed.
func TestIndexServesThePolicyTheBuildAuthored(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html": {Data: []byte(
			`<!doctype html><meta http-equiv="content-security-policy" content="` + metaCSP + `">shell`,
		)},
	})

	rec := get(t, h, "/messages/1")
	want := metaCSP + "; frame-ancestors 'none'"
	if got := rec.Header().Get("Content-Security-Policy"); got != want {
		t.Errorf("Content-Security-Policy = %q, want %q", got, want)
	}
}

// A shell without a meta tag is a hand-written index or a build that lost its
// policy; either way the app is served under the strict default.
func TestIndexFallsBackToTheDefaultPolicy(t *testing.T) {
	rec := get(t, handler(builtFS), "/")
	if got := rec.Header().Get("Content-Security-Policy"); got != defaultContentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want the default", got)
	}
}

// Assets are not documents: the policy of the page that loaded them applies.
func TestAssetsCarryNoPolicy(t *testing.T) {
	for _, path := range []string{"/_app/immutable/x.js", "/favicon.png"} {
		if got := get(t, handler(builtFS), path).Header().Get("Content-Security-Policy"); got != "" {
			t.Errorf("GET %s Content-Security-Policy = %q, want none", path, got)
		}
	}
}

func TestHandlerWithoutAnIndex(t *testing.T) {
	h := handler(fstest.MapFS{"favicon.png": {Data: []byte("icon")}})

	// Without the shell the app cannot run, so even files that do exist are
	// not worth serving.
	for _, path := range []string{"/", "/messages/1", "/favicon.png"} {
		if rec := get(t, h, path); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s = %d, want 503", path, rec.Code)
		}
	}
	if rec := get(t, h, "/api/unknown"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/unknown = %d, want 404", rec.Code)
	}
}

func TestHandlerNeverServesAPIPathsFromTheFS(t *testing.T) {
	rec := get(t, handler(fstest.MapFS{
		"index.html":   {Data: []byte("shell")},
		"api/leak.txt": {Data: []byte("secret")},
	}), "/api/leak.txt")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// The repository ships an empty web/build, so the handler must say so instead
// of pretending the app routes exist. Once `just web-build` has run, the same
// handler serves the real app, and this test follows whichever tree it finds.
func TestHandlerAgainstTheEmbeddedBuild(t *testing.T) {
	h := Handler()
	_, err := fs.Stat(build, indexFile)
	built := err == nil

	for _, path := range []string{"/", "/messages/1"} {
		rec := get(t, h, path)
		if !built {
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("GET %s = %d, want 503", path, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "UI not built") {
				t.Errorf("GET %s body = %q", path, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
			t.Errorf("GET %s served no Content-Security-Policy", path)
		}
	}
}

func TestHandlerNeverServesAPIPaths(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/unknown", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/unknown = %d, want 404", rec.Code)
	}
}
