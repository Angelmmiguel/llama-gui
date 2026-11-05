// Package web embeds the compiled SvelteKit application and serves it with the
// single-page-app fallback the router needs.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed all:build
var embedded embed.FS

// build is the embedded build directory. fs.Sub cannot fail for a directory
// that is embedded above.
var build = func() fs.FS {
	sub, err := fs.Sub(embedded, "build")
	if err != nil {
		panic(err)
	}
	return sub
}()

const indexFile = "index.html"

// defaultContentSecurityPolicy is the policy served when the shell carries no
// policy of its own. It is deliberately stricter than what the app needs: no
// inline script can run under it, so a build whose meta tag went missing fails
// visibly instead of silently losing its protection.
const defaultContentSecurityPolicy = "default-src 'self'; " +
	"img-src 'self' data: blob:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; " +
	"worker-src 'self' blob:; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'"

// cspMeta matches the meta tag SvelteKit emits for a hash-mode policy. The tag
// comes from the build, never from user input.
var cspMeta = regexp.MustCompile(`(?i)<meta[^>]+http-equiv="content-security-policy"[^>]+content="([^"]*)"`)

// Handler serves the embedded web app: real files as they are, every other
// path as index.html so client-side routing works on a cold load.
//
// If the app has not been built into web/build, it answers 503 instead of
// pretending the routes exist.
func Handler() http.Handler { return handler(build) }

// handler is Handler over an arbitrary file system, so the routing and caching
// rules can be tested without a real build.
func handler(fsys fs.FS) http.Handler {
	index, err := fs.ReadFile(fsys, indexFile)
	built := err == nil
	csp := contentSecurityPolicy(index)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The API is routed before this handler; anything left under /api is a
		// missing endpoint, never an SPA route.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if !built {
			http.Error(w, "UI not built", http.StatusServiceUnavailable)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || !fs.ValidPath(name) {
			serveIndex(w, r, fsys, csp)
			return
		}
		info, err := fs.Stat(fsys, name)
		if err != nil || info.IsDir() {
			serveIndex(w, r, fsys, csp)
			return
		}
		// Hashed asset paths never change contents, so they can be cached
		// forever; anything else is revalidated.
		if strings.HasPrefix(name, "_app/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, fsys, name)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS, csp string) {
	h := w.Header()
	// The shell embeds the app's entry points and must never be served stale.
	h.Set("Cache-Control", "no-store")
	// Only the document needs a policy; the assets it pulls in are governed by
	// the document that loaded them.
	h.Set("Content-Security-Policy", csp)
	http.ServeFileFS(w, r, fsys, indexFile)
}

// contentSecurityPolicy returns the policy for the app shell. SvelteKit writes
// the policy it authored into index.html as a meta tag, which cannot express
// frame-ancestors, so that directive is appended here.
func contentSecurityPolicy(index []byte) string {
	m := cspMeta.FindSubmatch(index)
	if m == nil {
		return defaultContentSecurityPolicy
	}
	return string(m[1]) + "; frame-ancestors 'none'"
}
