package library

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func bigGGUF() []byte {
	b := &ggufBuilder{}
	b.kvStr("general.architecture", "llama")
	b.key("tokenizer.ggml.token_type", 9)
	b.u32(0)
	b.u64(600 << 10)
	b.buf.Write(make([]byte, 600<<10))
	b.kvU32("llama.block_count", 12)
	return b.bytes(3, 1)
}

// fakeHub serves the subset of the hub API the client uses.
func fakeHub(t *testing.T, gguf []byte, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		q := r.URL.Query()
		if q.Get("filter") != "gguf" || q.Get("search") != "qwen" || q.Get("limit") != "5" || q.Get("sort") != "downloads" {
			t.Errorf("query %v", q)
		}
		fmt.Fprint(w, `[{"id":"a/b","author":"a","downloads":10,"likes":2,"gated":"auto","tags":["gguf"]},{"id":"../evil","downloads":1}]`)
	})
	mux.HandleFunc("/api/models/a/b", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"a/b","author":"a","downloads":10,"gated":false}`)
	})
	mux.HandleFunc("/api/models/a/b/tree/main", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<http://`+r.Host+`/api/models/a/b/tree/main?page=2>; rel="next"`)
			fmt.Fprint(w, `[{"type":"directory","path":"BF16"},{"type":"file","path":"README.md","size":3},
			{"type":"file","path":"m-Q8_0.gguf","size":800,"lfs":{"oid":"abc","size":800}},
			{"type":"file","path":"m-Q4_K_M.gguf","size":400},
			{"type":"file","path":"mmproj-m-f16.gguf","size":50},
			{"type":"file","path":"../x.gguf","size":1}]`)
			return
		}
		fmt.Fprint(w, `[{"type":"file","path":"BF16/m-BF16-00002-of-00002.gguf","size":1000},
		{"type":"file","path":"BF16/m-BF16-00001-of-00002.gguf","size":1000}]`)
	})
	mux.HandleFunc("/a/b/resolve/main/m.gguf", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var from, to int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &from, &to); err != nil {
			t.Errorf("range %q", r.Header.Get("Range"))
		}
		to = min(to, len(gguf)-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(gguf)))
		w.Header().Set("Content-Length", strconv.Itoa(to-from+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(gguf[from : to+1])
	})
	mux.HandleFunc("/gated/repo/resolve/main/m.gguf", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	mux.HandleFunc("/api/models/limited/repo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(429)
	})
	mux.HandleFunc("/api/models/gone/repo", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSearchAndCache(t *testing.T) {
	var hits atomic.Int32
	srv := fakeHub(t, nil, &hits)
	c := NewClient(srv.Client(), srv.URL, "")
	now := time.Now()
	c.now = func() time.Time { return now }
	for range 2 {
		res, err := c.Search(context.Background(), "qwen", "", "", 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Results) != 1 || res.Results[0].Repo != "a/b" || !res.Results[0].Gated || res.Results[0].Downloads != 10 {
			t.Fatalf("%+v", res)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("cache missed: %d hits", hits.Load())
	}
	now = now.Add(time.Hour)
	if _, err := c.Search(context.Background(), "qwen", "", "", 5); err != nil || hits.Load() != 2 {
		t.Errorf("expiry: %v %d", err, hits.Load())
	}
	if _, err := c.Search(context.Background(), "x", "bad filter!", "", 5); err == nil {
		t.Error("filter not validated")
	}
}

func TestRepoVariants(t *testing.T) {
	var hits atomic.Int32
	srv := fakeHub(t, nil, &hits)
	c := NewClient(srv.Client(), srv.URL, "")
	info, err := c.Repo(context.Background(), "a/b")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Variants) != 3 || len(info.MMProj) != 1 {
		t.Fatalf("%+v", info)
	}
	v := info.Variants
	if v[0].Quant != "Q4_K_M" || v[1].Quant != "Q8_0" || v[1].Files[0].SHA256 != "abc" {
		t.Errorf("variants %+v", v)
	}
	if v[2].Quant != "BF16" || v[2].Shards != 2 || v[2].SizeBytes != 2000 || v[2].Files[0].Name != "BF16/m-BF16-00001-of-00002.gguf" {
		t.Errorf("sharded %+v", v[2])
	}
	for _, bad := range []string{"", "a", "a/b/c", "../b", "a/..", "a/b c", "-a/b"} {
		if _, err := c.Repo(context.Background(), bad); !errors.Is(err, ErrInvalidRepo) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := fakeHub(t, nil, &hits)
	c := NewClient(srv.Client(), srv.URL, "")
	if _, err := c.Repo(context.Background(), "limited/repo"); !errors.Is(err, ErrRateLimited) || !strings.Contains(err.Error(), "42") {
		t.Errorf("429: %v", err)
	}
	if _, err := c.Repo(context.Background(), "gone/repo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("404: %v", err)
	}
	if _, err := c.ReadRemoteMetadata(context.Background(), "gated/repo", "m.gguf"); !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "token") {
		t.Errorf("403: %v", err)
	}
	if _, err := c.ReadRemoteMetadata(context.Background(), "a/b", "../m.gguf"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("name: %v", err)
	}
}

func TestReadRemoteMetadataGrows(t *testing.T) {
	var hits atomic.Int32
	srv := fakeHub(t, bigGGUF(), &hits)
	c := NewClient(srv.Client(), srv.URL, "")
	m, err := c.ReadRemoteMetadata(context.Background(), "a/b", "m.gguf")
	if err != nil {
		t.Fatal(err)
	}
	if m.Architecture != "llama" || m.BlockCount != 12 {
		t.Errorf("%+v", m)
	}
	if hits.Load() != 2 {
		t.Errorf("expected a second, larger request, got %d", hits.Load())
	}
	if _, err := c.ReadRemoteMetadata(context.Background(), "a/b", "m.gguf"); err != nil || hits.Load() != 2 {
		t.Errorf("not cached: %v %d", err, hits.Load())
	}
}

func TestReadRemoteMetadataTruncatedFile(t *testing.T) {
	var hits atomic.Int32
	srv := fakeHub(t, sampleGGUF(3)[:40], &hits)
	c := NewClient(srv.Client(), srv.URL, "")
	if _, err := c.ReadRemoteMetadata(context.Background(), "a/b", "m.gguf"); !errors.Is(err, ErrTruncated) {
		t.Errorf("got %v", err)
	}
}

func TestTokenSent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	if _, err := NewClient(srv.Client(), srv.URL, "hf_x").Search(context.Background(), "", "", "", 1); err != nil || got != "Bearer hf_x" {
		t.Errorf("%q %v", got, err)
	}
}

func TestRealHub(t *testing.T) {
	if os.Getenv("LLAMA_GUI_NETWORK_TESTS") == "" {
		t.Skip("set LLAMA_GUI_NETWORK_TESTS=1 to reach huggingface.co")
	}
	c := NewClient(nil, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := c.Repo(ctx, "ggml-org/SmolVLM-256M-Instruct-GGUF")
	if err != nil {
		t.Skip("hub unreachable:", err)
	}
	t.Logf("%+v", info)
	m, err := c.ReadRemoteMetadata(ctx, "ggml-org/SmolVLM-256M-Instruct-GGUF", "SmolVLM-256M-Instruct-Q8_0.gguf")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", m)
	if m.Architecture != "llama" || m.Quant != "Q8_0" || m.BlockCount != 30 {
		t.Errorf("%+v", m)
	}
}

func TestSearchFollowsTheCursor(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query().Get("cursor"))
		if r.URL.Query().Get("cursor") == "" {
			// A hostile host in the link must not move the next request.
			w.Header().Set("Link", `<https://evil.example/api/models?limit=2&cursor=abc%3D>; rel="next"`)
		}
		_, _ = w.Write([]byte(`[{"id":"a/b"}]`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.Client(), srv.URL, "")
	first, err := c.Search(t.Context(), "q", "", "", 2)
	if err != nil || first.Next != "abc=" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := c.Search(t.Context(), "q", "", first.Next, 2)
	if err != nil || second.Next != "" || len(second.Results) != 1 {
		t.Fatalf("second page: %+v %v", second, err)
	}
	if want := []string{"", "abc="}; !slices.Equal(got, want) {
		t.Errorf("cursors = %v, want %v", got, want)
	}
}

func TestNextCursor(t *testing.T) {
	for link, want := range map[string]string{
		"": "",
		`<https://huggingface.co/api/models?cursor=x>; rel="prev"`:                                         "",
		`<https://huggingface.co/api/models?limit=2>; rel="next"`:                                          "",
		`<https://huggingface.co/api/models?cursor=x>; rel="next"`:                                         "x",
		`<https://huggingface.co/m?cursor=a>; rel="prev", <https://huggingface.co/m?cursor=b>; rel="next"`: "b",
	} {
		if got := nextCursor(link); got != want {
			t.Errorf("%q: got %q, want %q", link, got, want)
		}
	}
}
