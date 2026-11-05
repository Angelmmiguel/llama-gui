package app

import (
	"net/http"
	"strings"
)

// routes is the one place requests are split between the dashboard, the API and
// the router. It is a plain switch rather than a ServeMux because the mux
// answers unclean paths with a redirect, and proxied paths must reach upstream
// as the client sent them.
func routes(apiHandler, ui, proxy http.Handler) http.Handler {
	stripped := http.StripPrefix("/ui", ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func acceptsHTML(r *http.Request) bool {
	return strings.Contains(strings.ToLower(strings.Join(r.Header.Values("Accept"), ",")), "text/html")
}
