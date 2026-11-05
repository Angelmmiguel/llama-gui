package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubs records which handler a request reached and the path it saw.
func stubs() (http.Handler, *string, *string) {
	var hit, seen string
	stub := func(name string) http.Handler {
		return http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			hit, seen = name, r.URL.Path
		})
	}
	return routes(stub("api"), stub("ui"), stub("proxy")), &hit, &seen
}

func TestRouteTable(t *testing.T) {
	const html = "text/html,application/xhtml+xml"
	for _, tc := range []struct {
		method, target, accept string
		hit, seen              string
	}{
		{"GET", "/ui/", html, "ui", "/"},
		{"GET", "/ui/requests", html, "ui", "/requests"},
		{"GET", "/ui/_app/immutable/x.js", "*/*", "ui", "/_app/immutable/x.js"},
		{"GET", "/api/v1/status", "*/*", "api", "/api/v1/status"},
		{"POST", "/api/v1/server/start", "*/*", "api", "/api/v1/server/start"},
		{"GET", "/api/v1/status", html, "api", "/api/v1/status"},

		// Upstream paths that a browser or SPA fallback must never swallow.
		{"GET", "/slots", "*/*", "proxy", "/slots"},
		{"GET", "/slots", html, "proxy", "/slots"},
		{"GET", "/metrics", "*/*", "proxy", "/metrics"},
		{"GET", "/models", "*/*", "proxy", "/models"},
		{"GET", "/props", "*/*", "proxy", "/props"},
		{"POST", "/v1/chat/completions", "*/*", "proxy", "/v1/chat/completions"},
		{"GET", "/", "*/*", "proxy", "/"},
		{"GET", "/", "", "proxy", "/"},
		{"POST", "/", html, "proxy", "/"},
		{"HEAD", "/", html, "proxy", "/"},

		// Only whole path segments match the prefixes.
		{"GET", "/uix", html, "proxy", "/uix"},
		{"GET", "/apifoo", "*/*", "proxy", "/apifoo"},
		{"GET", "/api", "*/*", "proxy", "/api"},

		// Proxied paths reach upstream unchanged, not cleaned or redirected.
		{"POST", "//v1//completions", "*/*", "proxy", "//v1//completions"},
	} {
		t.Run(tc.method+" "+tc.target+" "+tc.accept, func(t *testing.T) {
			h, hit, seen := stubs()
			r := httptest.NewRequest(tc.method, tc.target, nil)
			if tc.accept != "" {
				r.Header.Set("Accept", tc.accept)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if *hit != tc.hit || *seen != tc.seen {
				t.Errorf("reached %q with %q, want %q with %q (status %d)", *hit, *seen, tc.hit, tc.seen, w.Code)
			}
		})
	}
}

func TestRedirects(t *testing.T) {
	for _, tc := range []struct{ target, accept, location string }{
		{"/ui", "*/*", "/ui/"},
		{"/ui?a=b", "*/*", "/ui/?a=b"},
		{"/", "text/html", "/ui/"},
		{"/", "Text/HTML;q=0.9", "/ui/"},
	} {
		h, hit, _ := stubs()
		r := httptest.NewRequest(http.MethodGet, tc.target, nil)
		r.Header.Set("Accept", tc.accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusFound || w.Header().Get("Location") != tc.location {
			t.Errorf("%s: %d to %q, want a redirect to %q", tc.target, w.Code, w.Header().Get("Location"), tc.location)
		}
		if *hit != "" {
			t.Errorf("%s reached %s", tc.target, *hit)
		}
	}
}

func TestProxyStub(t *testing.T) {
	w := httptest.NewRecorder()
	proxyStub().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slots", nil))
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusServiceUnavailable || body["error"] != "llama-server is not running" {
		t.Errorf("%d %v", w.Code, body)
	}
}
