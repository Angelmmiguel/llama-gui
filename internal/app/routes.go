package app

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// routes is the one place requests are split between the dashboard, the API and
// the router. It is a plain switch rather than a ServeMux because the mux
// answers unclean paths with a redirect, and proxied paths must reach upstream
// as the client sent them.
func routes(allowedHosts []string, apiHandler, ui, proxy http.Handler) http.Handler {
	stripped := http.StripPrefix("/ui", ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host := hostName(r.Host); !hostAllowed(host, allowedHosts) {
			http.Error(w, fmt.Sprintf("Host %q is not allowed; add it to LLAMA_GUI_ALLOWED_HOSTS", host), http.StatusForbidden)
			return
		}
		path := r.URL.Path
		switch {
		case path == "/ui":
			target := "/ui/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // G710: the target is a fixed local path; only the query is carried over.
		case strings.HasPrefix(path, "/ui/"):
			stripped.ServeHTTP(w, r)
		case strings.HasPrefix(path, "/api/"):
			apiHandler.ServeHTTP(w, r)
		case path == "/" && r.Method == http.MethodGet && acceptsHTML(r):
			http.Redirect(w, r, "/ui/", http.StatusFound)
		default:
			proxy.ServeHTTP(w, r)
		}
	})
}

// privateSuffixes are names that public DNS does not hand out, so a web page
// cannot rebind one of them to this server.
var privateSuffixes = []string{
	".local", ".lan", ".home", ".internal", ".intranet", ".corp", ".localdomain", ".home.arpa",
}

// hostName reduces a Host header to the lowercase name, without port or brackets.
func hostName(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// hostAllowed stops DNS rebinding, where a page on a public name that now resolves
// here would count as same-origin. Names that cannot be rebound pass on their own,
// and so does a request without a Host, which no browser sends.
func hostAllowed(host string, allowed []string) bool {
	if !strings.Contains(host, ".") || slices.Contains(allowed, "*") || slices.Contains(allowed, host) {
		return true
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	return slices.ContainsFunc(privateSuffixes, func(s string) bool { return strings.HasSuffix(host, s) })
}

func acceptsHTML(r *http.Request) bool {
	return strings.Contains(strings.ToLower(strings.Join(r.Header.Values("Accept"), ",")), "text/html")
}
