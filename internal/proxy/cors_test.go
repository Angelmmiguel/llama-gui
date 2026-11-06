package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	siteA = "https://chat.example.com"
	siteB = "https://evil.example"
)

func TestBrowserAccess(t *testing.T) {
	type headers = map[string]string
	allowA := []string{siteA}
	star := []string{"*"}
	cross := func(origin string) headers { return headers{"Sec-Fetch-Site": "cross-site", "Origin": origin} }
	for _, tc := range []struct {
		name         string
		method, path string
		headers      headers
		allowed      []string
		status       int // 0 means it reached upstream
		acao         string
	}{
		{name: "curl write", method: "POST", path: "/models/load"},
		{name: "curl inference", method: "POST", path: "/v1/chat/completions"},
		{name: "curl options", method: "OPTIONS", path: "/models/load"},
		{name: "typed in", method: "POST", path: "/models/load", headers: headers{"Sec-Fetch-Site": "none"}},
		{name: "dashboard", method: "POST", path: "/models/load", headers: headers{"Sec-Fetch-Site": "same-origin", "Origin": "http://app.test"}},
		{name: "same origin wins over a mismatched Origin", method: "POST", path: "/slots/0?action=erase", headers: headers{"Sec-Fetch-Site": "same-origin", "Origin": siteB}},
		{name: "plain http same origin", method: "POST", path: "/models/load", headers: headers{"Origin": "http://APP.test"}},

		{name: "cross-site admin", method: "POST", path: "/models/load", headers: cross(siteB), status: 403},
		{name: "same-site admin", method: "POST", path: "/models/load", headers: headers{"Sec-Fetch-Site": "same-site", "Origin": siteB}, status: 403},
		{name: "unknown fetch site", method: "POST", path: "/models/load", headers: headers{"Sec-Fetch-Site": "other", "Origin": siteB}, status: 403},
		{name: "cross-site inference, nothing allowed", method: "POST", path: "/v1/chat/completions", headers: cross(siteB), status: 403},
		{name: "plain http other origin", method: "POST", path: "/slots/0?action=erase", headers: headers{"Origin": siteB}, status: 403},
		{name: "plain http other port", method: "POST", path: "/slots/0?action=erase", headers: headers{"Origin": "http://app.test:81"}, status: 403},
		{name: "plain http null origin", method: "POST", path: "/completion", headers: headers{"Origin": "null"}, status: 403},
		{name: "plain http bad origin", method: "POST", path: "/completion", headers: headers{"Origin": "::"}, status: 403},
		{name: "cross-site without Origin", method: "POST", path: "/completion", headers: headers{"Sec-Fetch-Site": "cross-site"}, allowed: star, status: 403},

		{name: "allowed origin on inference", method: "POST", path: "/v1/chat/completions", headers: cross(siteA), allowed: allowA, acao: siteA},
		{name: "allowed origin over plain http", method: "POST", path: "/completion", headers: headers{"Origin": siteA}, allowed: allowA, acao: siteA},
		{name: "allowed origin, audio route", method: "POST", path: "/v1/audio/speech", headers: cross(siteA), allowed: allowA, acao: siteA},
		{name: "other origin on inference", method: "POST", path: "/v1/chat/completions", headers: cross(siteB), allowed: allowA, status: 403},
		{name: "star on inference", method: "POST", path: "/v1/chat/completions", headers: cross(siteB), allowed: star, acao: siteB},

		{name: "allowed origin on /models/load", method: "POST", path: "/models/load", headers: cross(siteA), allowed: allowA, status: 403},
		{name: "star on /models/load", method: "POST", path: "/models/load", headers: cross(siteB), allowed: star, status: 403},
		{name: "star on /slots", method: "POST", path: "/slots/0?action=save", headers: cross(siteB), allowed: star, status: 403},
		{name: "star on /props", method: "POST", path: "/props", headers: cross(siteB), allowed: star, status: 403},
		{name: "star on /cors-proxy", method: "POST", path: "/cors-proxy", headers: cross(siteB), allowed: star, status: 403},
		{name: "star on /tools", method: "POST", path: "/tools", headers: cross(siteB), allowed: star, status: 403},
		{name: "star on /lora-adapters", method: "POST", path: "/lora-adapters", headers: cross(siteB), allowed: star, status: 403},
		{name: "star through dot segments", method: "POST", path: "/v1/audio/..%2f..%2fmodels/load", headers: cross(siteB), allowed: star, status: 403},

		{name: "preflight for an allowed origin", method: "OPTIONS", path: "/v1/chat/completions", headers: cross(siteA), allowed: allowA, status: 204, acao: siteA},
		{name: "preflight with star", method: "OPTIONS", path: "/v1/chat/completions", headers: cross(siteB), allowed: star, status: 204, acao: siteB},
		{name: "preflight for another origin", method: "OPTIONS", path: "/v1/chat/completions", headers: cross(siteB), allowed: allowA, status: 403},
		{name: "preflight for an admin route", method: "OPTIONS", path: "/models/load", headers: cross(siteA), allowed: allowA, status: 403},
		{name: "preflight with star for an admin route", method: "OPTIONS", path: "/slots/0", headers: cross(siteB), allowed: star, status: 403},

		{name: "cross-site read gets no CORS headers", method: "GET", path: "/slots", headers: cross(siteB)},
		{name: "allowed origin's read gets no CORS headers", method: "GET", path: "/v1/chat/completions", headers: cross(siteA), allowed: allowA},
		{name: "cross-site HEAD", method: "HEAD", path: "/props", headers: cross(siteB)},
		{name: "cross-site websocket", method: "GET", path: "/v1/realtime", headers: headers{"Sec-Fetch-Site": "cross-site", "Origin": siteB, "Upgrade": "websocket", "Connection": "Upgrade"}, allowed: star, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reached atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached.Store(true)
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", "*")
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", "GET, POST")
				h.Set("X-Upstream", "kept")
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			front := httptest.NewServer(New(Options{
				Upstream:       func() (*url.URL, bool) { return target, true },
				AllowedOrigins: tc.allowed,
			}))
			defer front.Close()

			req, err := http.NewRequest(tc.method, front.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "app.test"
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := front.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body := drain(t, resp)

			wantUpstream := tc.status == 0
			if reached.Load() != wantUpstream {
				t.Errorf("reached upstream = %v, want %v", reached.Load(), wantUpstream)
			}
			if wantUpstream && resp.Header.Get("X-Upstream") != "kept" {
				t.Errorf("other upstream headers are lost: %v", resp.Header)
			}
			if !wantUpstream && resp.StatusCode != tc.status {
				t.Errorf("status %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.status == http.StatusForbidden && !strings.Contains(body, `"code":403`) {
				t.Errorf("not upstream's error shape: %s", body)
			}
			got := resp.Header.Values("Access-Control-Allow-Origin")
			if (tc.acao == "" && len(got) != 0) || (tc.acao != "" && (len(got) != 1 || got[0] != tc.acao)) {
				t.Errorf("Access-Control-Allow-Origin = %v, want %q", got, tc.acao)
			}
			if tc.acao != "" && !strings.Contains(strings.Join(resp.Header.Values("Vary"), ","), "Origin") {
				t.Errorf("no Vary: Origin in %v", resp.Header)
			}
			if resp.Header.Get("Access-Control-Allow-Credentials") != "" {
				t.Errorf("upstream's CORS headers were forwarded: %v", resp.Header)
			}
			if tc.status != http.StatusNoContent && resp.Header.Get("Access-Control-Allow-Methods") != "" {
				t.Errorf("upstream's CORS headers were forwarded: %v", resp.Header)
			}
		})
	}
}

func TestPreflightAnswer(t *testing.T) {
	p := New(Options{AllowedOrigins: []string{siteA}, Upstream: func() (*url.URL, bool) { return nil, false }})
	r := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Origin", siteA)
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	for k, want := range map[string]string{
		"Access-Control-Allow-Origin":  siteA,
		"Access-Control-Allow-Methods": "POST",
		"Access-Control-Allow-Headers": "authorization,content-type",
		"Vary":                         "Origin",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// An allowed origin has to be able to read why a request failed.
func TestAllowedOriginReadsErrors(t *testing.T) {
	p := New(Options{AllowedOrigins: []string{siteA}, Upstream: func() (*url.URL, bool) { return nil, false }})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Origin", siteA)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Access-Control-Allow-Origin") != siteA {
		t.Errorf("%d %v", w.Code, w.Header())
	}
}
