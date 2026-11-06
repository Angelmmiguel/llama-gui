package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

// stubs records which handler a request reached and the path it saw.
func stubs() (http.Handler, *string, *string) {
	var hit, seen string
	stub := func(name string) http.Handler {
		return http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			hit, seen = name, r.URL.Path
		})
	}
	return routes([]string{"example.com"}, stub("api"), stub("ui"), stub("proxy")), &hit, &seen
}

func TestHostCheck(t *testing.T) {
	const message = "add it to LLAMA_GUI_ALLOWED_HOSTS"
	extra := []string{"llm.example.com"}
	for _, tc := range []struct {
		host    string
		allowed []string
		ok      bool
	}{
		{"127.0.0.1", nil, true},
		{"127.0.0.1:8080", nil, true},
		{"192.168.1.20:8080", nil, true},
		{"[::1]", nil, true},
		{"[::1]:8080", nil, true},
		{"[fe80::1%25eth0]:8080", nil, true},
		{"localhost", nil, true},
		{"LOCALHOST:8080", nil, true},
		{"tower", nil, true},
		{"llama-gui:8080", nil, true},
		{"tower.local", nil, true},
		{"Tower.LOCAL:8080", nil, true},
		{"tower.local.", nil, true},
		{"a.lan", nil, true},
		{"a.home", nil, true},
		{"a.internal", nil, true},
		{"a.intranet", nil, true},
		{"a.corp", nil, true},
		{"a.localdomain", nil, true},
		{"a.home.arpa", nil, true},

		{"evil.com", nil, false},
		{"evil.com:8080", nil, false},
		{"EVIL.com", nil, false},
		{"evil.com.", nil, false},
		{"tower.local.evil.com", nil, false},
		{"127.0.0.1.nip.io", nil, false},
		{"notlocal", nil, true},
		{"evillocal.com", nil, false},
		{"evil.home.com", nil, false},
		{"arpa.evil", nil, false},

		{"llm.example.com", extra, true},
		{"LLM.Example.com:443", extra, true},
		{"other.example.com", extra, false},
		{"sub.llm.example.com", extra, false},
		{"anything.example.com", []string{"*"}, true},
	} {
		t.Run(tc.host, func(t *testing.T) {
			for _, path := range []string{"/api/v1/status", "/ui/", "/slots"} {
				reached := false
				stub := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Host = tc.host
				w := httptest.NewRecorder()
				routes(tc.allowed, stub, stub, stub).ServeHTTP(w, r)
				if reached != tc.ok {
					t.Errorf("%s: reached = %v, want %v", path, reached, tc.ok)
				}
				if !tc.ok {
					if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), message) {
						t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
					}
					if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
						t.Errorf("%s: content type %q", path, ct)
					}
				}
			}
		})
	}
}

func TestHostCheckNamesTheHostWithoutThePort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "Evil.com:8080"
	w := httptest.NewRecorder()
	stub := http.NotFoundHandler()
	routes(nil, stub, stub, stub).ServeHTTP(w, r)
	const want = `Host "evil.com" is not allowed; add it to LLAMA_GUI_ALLOWED_HOSTS` + "\n"
	if w.Body.String() != want {
		t.Errorf("got %q, want %q", w.Body.String(), want)
	}
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

func TestInstallRetryDelay(t *testing.T) {
	now := time.Unix(1000, 0)
	limited := fmt.Errorf("resolve: %w", &releases.RateLimitError{Reset: now.Add(20 * time.Minute)})
	if got, want := installRetryDelay(limited, 0, now), 20*time.Minute+5*time.Second; got != want {
		t.Errorf("rate limited: %v, want %v", got, want)
	}
	expired := &releases.RateLimitError{Reset: now.Add(-time.Minute)}
	if got := installRetryDelay(expired, 0, now); got != time.Minute {
		t.Errorf("lifted rate limit: %v", got)
	}
	other := errors.New("no network")
	for attempt, want := range map[int]time.Duration{0: time.Minute, 2: 4 * time.Minute, 5: 30 * time.Minute, 60: 30 * time.Minute} {
		if got := installRetryDelay(other, attempt, now); got != want {
			t.Errorf("attempt %d: %v, want %v", attempt, got, want)
		}
	}
}
