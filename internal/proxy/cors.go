package proxy

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

const preflightMaxAge = "600"

// CrossSite says a browser sent this request on behalf of another site. Only
// browsers set Sec-Fetch-Site and Origin, so other clients are never cross-site.
// Browsers leave out Sec-Fetch-Site on plain HTTP to a LAN host, where Origin decides.
func CrossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		u, err := url.Parse(origin)
		return err != nil || !strings.EqualFold(u.Host, r.Host)
	}
	return true
}

// refuseCrossSite applies the rules under "Browser access" in docs/ARCHITECTURE.md,
// and reports whether it answered the request itself.
func (p *Proxy) refuseCrossSite(w http.ResponseWriter, r *http.Request) bool {
	if !CrossSite(r) {
		return false
	}
	// A WebSocket handshake is a GET, but CORS does not stop the page reading the answer.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Header.Get("Upgrade") == "" {
		return false
	}
	origin := r.Header.Get("Origin")
	if !p.originAllowed(origin) || !isInference(r.URL.Path) {
		writeError(w, http.StatusForbidden, "permission_error", "cross-site requests to this route are not allowed")
		return true
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Add("Vary", "Origin")
	if r.Method != http.MethodOptions {
		return false
	}
	h.Set("Access-Control-Allow-Methods", http.MethodPost)
	if asked := r.Header.Get("Access-Control-Request-Headers"); asked != "" {
		h.Set("Access-Control-Allow-Headers", asked)
	}
	h.Set("Access-Control-Max-Age", preflightMaxAge)
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (p *Proxy) originAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	return slices.Contains(p.opts.AllowedOrigins, "*") || slices.Contains(p.opts.AllowedOrigins, origin)
}

// stripCORS removes upstream's answers, which allow every origin on every route.
func stripCORS(h http.Header) {
	for name := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "Access-Control-") {
			h.Del(name)
		}
	}
}
