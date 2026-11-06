package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// A few releases in GitHub's shape, so the demo's Builds page has something to show without using
// up the 60 anonymous API requests an hour that everyone behind an address shares.
const (
	demoStable  = "v0.9.0"
	demoBinary  = "b12110"
	demoNightly = "b12144"
)

// serveFakeGitHub answers what internal/releases asks, and returns its base URL.
func serveFakeGitHub() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	base := "http://" + ln.Addr().String()

	release := func(tag string, age time.Duration) map[string]any {
		var assets []map[string]any
		if strings.HasPrefix(tag, "v") {
			assets = append(assets, map[string]any{
				"name": "nightly-tag.txt", "size": len(demoBinary), "browser_download_url": base + "/assets/nightly-tag.txt",
			})
		} else {
			for _, suffix := range []string{"cuda-12.8-x64", "vulkan-x64", "x64"} {
				name := fmt.Sprintf("llama-%s-bin-ubuntu-%s.tar.gz", tag, suffix)
				assets = append(assets, map[string]any{
					"name": name, "size": 180 << 20, "browser_download_url": base + "/assets/" + name,
				})
			}
			runtime := fmt.Sprintf("cudart-llama-%s-bin-ubuntu-cuda-12.8-x64.tar.gz", tag)
			assets = append(assets, map[string]any{
				"name": runtime, "size": 560 << 20, "browser_download_url": base + "/assets/" + runtime,
			})
		}
		return map[string]any{
			"tag_name": tag, "draft": false, "published_at": time.Now().Add(-age).UTC().Format(time.RFC3339),
			"body": "A release of the demo's fake GitHub.", "assets": assets,
		}
	}
	all := []map[string]any{
		release(demoNightly, 5*24*time.Hour),
		release(demoStable, 12*24*time.Hour),
		release(demoBinary, 12*24*time.Hour),
	}
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	const repo = "/repos/ggml-org/llama.cpp"
	mux := http.NewServeMux()
	mux.HandleFunc(repo+"/releases", func(w http.ResponseWriter, _ *http.Request) { reply(w, all) })
	mux.HandleFunc(repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) { reply(w, all[1]) })
	mux.HandleFunc(repo+"/releases/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		for _, rel := range all {
			if rel["tag_name"] == r.PathValue("tag") {
				reply(w, rel)
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc(repo+"/compare/{range}", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, map[string]any{"ahead_by": 34, "commits": []any{}})
	})
	mux.HandleFunc("/assets/nightly-tag.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, demoBinary)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return base, nil
}
