package releases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub serves pages of releases shaped like the real API.
type fakeGitHub struct {
	*httptest.Server
	mu       sync.Mutex
	pages    [][]string
	requests []*http.Request
	status   func(r *http.Request, w http.ResponseWriter) bool
}

func releaseJSON(srv string, tag string) map[string]any {
	published := "2026-11-04T13:08:06Z"
	var assets []map[string]any
	switch ClassifyTag(tag) {
	case ChannelStable:
		assets = append(assets, map[string]any{
			"name": nightlyAsset, "size": 7, "digest": "sha256:aa",
			"browser_download_url": srv + "/download/" + tag + "/" + nightlyAsset,
			"uploader":             map[string]any{"login": "x"},
		})
	default:
		assets = append(assets, map[string]any{
			"name": "llama-" + tag + "-bin-ubuntu-x64.tar.gz", "size": 10, "digest": nil,
			"browser_download_url": srv + "/download/" + tag + "/a.tar.gz",
		})
		if n, err := strconv.Atoi(strings.TrimPrefix(tag, "b")); err == nil {
			published = time.Date(2026, 11, 1, 0, 0, n, 0, time.UTC).Format(time.RFC3339)
		}
	}
	return map[string]any{
		"tag_name": tag, "prerelease": tag[0] == 'b', "draft": false, "published_at": published,
		"body": "notes of " + tag, "assets": assets, "unknown_field": 1,
	}
}

func newFake(t *testing.T, pages ...[]string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{pages: pages}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(context.Background()))
	f.mu.Unlock()
	if f.status != nil && f.status(r, w) {
		return
	}
	srv := "http://" + r.Host
	path := r.URL.Path
	switch {
	case path == repoPath+"/releases":
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		page = max(page, 1)
		if page > len(f.pages) {
			fmt.Fprint(w, "[]")
			return
		}
		if page < len(f.pages) {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s/releases?per_page=100&page=%d>; rel="next", <%s/last>; rel="last"`, srv, repoPath, page+1, srv))
		}
		items := []map[string]any{}
		for _, tag := range f.pages[page-1] {
			items = append(items, releaseJSON(srv, tag))
		}
		_ = json.NewEncoder(w).Encode(items)
	case path == repoPath+"/releases/latest":
		for _, p := range f.pages {
			for _, tag := range p {
				if ClassifyTag(tag) == ChannelStable {
					_ = json.NewEncoder(w).Encode(releaseJSON(srv, tag))
					return
				}
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(path, repoPath+"/releases/tags/"):
		tag := strings.TrimPrefix(path, repoPath+"/releases/tags/")
		for _, p := range f.pages {
			for _, have := range p {
				if have == tag {
					_ = json.NewEncoder(w).Encode(releaseJSON(srv, tag))
					return
				}
			}
		}
		http.NotFound(w, r)
	case strings.HasSuffix(path, "/"+nightlyAsset):
		fmt.Fprint(w, "b12110\n")
	case strings.HasPrefix(path, repoPath+"/compare/"):
		fmt.Fprint(w, `{"ahead_by":300,"behind_by":0,"commits":[
			{"sha":"abc","commit":{"message":"server : fix a thing (#1)\n\nSigned-off-by: x"}},
			{"sha":"def","commit":{"message":"docs"}}]}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func tags(rs []Release) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Tag
	}
	return strings.Join(out, " ")
}

func TestClassifyAndValidateTag(t *testing.T) {
	for tag, want := range map[string]Channel{
		"v0.9.0":                      ChannelStable,
		"v1":                          ChannelStable,
		"v1.2-rc1":                    ChannelStable,
		"b12110":                      ChannelNightly,
		"master-3bcc129":              ChannelNone,
		"":                            ChannelNone,
		"b12x":                        ChannelNone,
		"b1/../../etc":                ChannelNone,
		"v1/../x":                     ChannelNone,
		"v1..2":                       ChannelNone,
		"b12110\n":                    ChannelNone,
		"v":                           ChannelNone,
		"vx":                          ChannelNone,
		"v" + strings.Repeat("1", 64): ChannelNone,
	} {
		if got := ClassifyTag(tag); got != want {
			t.Errorf("ClassifyTag(%q) = %q, want %q", tag, got, want)
		}
		if err := ValidateTag(tag); (err == nil) != (want != ChannelNone) || (err != nil && !errors.Is(err, ErrInvalidTag)) {
			t.Errorf("ValidateTag(%q) = %v", tag, err)
		}
	}
}

func TestListWalksPagesForStable(t *testing.T) {
	f := newFake(t,
		[]string{"b5", "b4", "master-x"},
		[]string{"b3", "v0.9.0", "b2"},
		[]string{"b1"},
		[]string{"v0.8.0"},
	)
	c := NewClient(f.Client(), f.URL, "")
	got, err := c.List(context.Background(), ChannelStable, 1)
	if err != nil || tags(got) != "v0.9.0" {
		t.Fatalf("got %q, %v", tags(got), err)
	}
	if f.count() != 2 {
		t.Errorf("requests = %d, want to stop at page 2", f.count())
	}
	if got[0].BinaryTag != "" || got[0].Channel != ChannelStable {
		t.Errorf("release = %+v", got[0])
	}

	all, err := c.List(context.Background(), ChannelStable, 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("got %q, %v", tags(all), err)
	}
	n, err := c.List(context.Background(), ChannelNightly, 2)
	if err != nil || tags(n) != "b5 b4" || n[0].BinaryTag != "b5" {
		t.Fatalf("got %q, %v", tags(n), err)
	}
}

func TestListOrdersNightliesByNumber(t *testing.T) {
	f := newFake(t, []string{"b9", "b10", "b100"})
	got, err := NewClient(f.Client(), f.URL, "").List(context.Background(), ChannelNightly, 0)
	if err != nil || tags(got) != "b100 b10 b9" {
		t.Fatalf("got %q, %v", tags(got), err)
	}
}

func TestListPageBound(t *testing.T) {
	pages := make([][]string, maxPages+5)
	for i := range pages {
		pages[i] = []string{"b" + strconv.Itoa(i+1)}
	}
	f := newFake(t, pages...)
	got, err := NewClient(f.Client(), f.URL, "").List(context.Background(), ChannelStable, 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %q, %v", tags(got), err)
	}
	if f.count() != maxPages {
		t.Errorf("requests = %d", f.count())
	}
}

func TestListFailsSoftOnLaterPage(t *testing.T) {
	f := newFake(t, []string{"v1.0.0"}, []string{"v0.9.0"})
	f.status = func(r *http.Request, w http.ResponseWriter) bool {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	}
	got, err := NewClient(f.Client(), f.URL, "").List(context.Background(), ChannelStable, 5)
	if err != nil || tags(got) != "v1.0.0" {
		t.Fatalf("got %q, %v", tags(got), err)
	}
}

func TestListFirstPageFailure(t *testing.T) {
	f := newFake(t)
	f.status = func(_ *http.Request, w http.ResponseWriter) bool {
		w.WriteHeader(http.StatusBadGateway)
		return true
	}
	if _, err := NewClient(f.Client(), f.URL, "").List(context.Background(), ChannelStable, 5); err == nil {
		t.Fatal("no error")
	}
}

func TestListIgnoresNextLinkToOtherHost(t *testing.T) {
	f := newFake(t, []string{"b1"})
	f.status = func(r *http.Request, w http.ResponseWriter) bool {
		w.Header().Set("Link", `<https://evil.example/steal>; rel="next"`)
		return false
	}
	if _, err := NewClient(f.Client(), f.URL, "secret").List(context.Background(), ChannelStable, 5); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Errorf("requests = %d", f.count())
	}
}

func TestResolveStable(t *testing.T) {
	f := newFake(t, []string{"b12111", "v0.9.0", "b12110"})
	rel, err := NewClient(f.Client(), f.URL, "").ResolveVersion(context.Background(), "stable")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.9.0" || rel.BinaryTag != "b12110" || rel.Channel != ChannelStable || rel.Body != "notes of v0.9.0" {
		t.Errorf("release = %+v", rel)
	}
	if len(rel.Assets) != 1 || rel.Assets[0].Name != "llama-b12110-bin-ubuntu-x64.tar.gz" {
		t.Errorf("assets = %+v", rel.Assets)
	}
}

func TestResolveNightlyAndTags(t *testing.T) {
	f := newFake(t, []string{"b12111", "v0.9.0", "b12110"})
	c := NewClient(f.Client(), f.URL, "")
	ctx := context.Background()
	if rel, err := c.ResolveVersion(ctx, "nightly"); err != nil || rel.Tag != "b12111" || rel.BinaryTag != "b12111" {
		t.Errorf("nightly = %+v, %v", rel, err)
	}
	if rel, err := c.ResolveVersion(ctx, "b12110"); err != nil || rel.BinaryTag != "b12110" {
		t.Errorf("b12110 = %+v, %v", rel, err)
	}
	if rel, err := c.ResolveVersion(ctx, "v0.9.0"); err != nil || rel.BinaryTag != "b12110" {
		t.Errorf("v0.9.0 = %+v, %v", rel, err)
	}
	if _, err := c.ResolveVersion(ctx, "b1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("b1 err = %v", err)
	}
	before := f.count()
	for _, bad := range []string{"../../etc", "master-3bcc129", "", "b1/x"} {
		if _, err := c.ResolveVersion(ctx, bad); !errors.Is(err, ErrInvalidTag) {
			t.Errorf("%q err = %v", bad, err)
		}
	}
	if f.count() != before {
		t.Error("invalid tags reached the network")
	}
}

func TestResolveRejectsBadNightlyTagFile(t *testing.T) {
	for _, content := range []string{"../../evil\n", "b12 \n", "", "v1.0\n", "b1\nb2\n", strings.Repeat("1", 100)} {
		f := newFake(t, []string{"v0.9.0"})
		f.status = func(r *http.Request, w http.ResponseWriter) bool {
			if strings.HasSuffix(r.URL.Path, "/"+nightlyAsset) {
				fmt.Fprint(w, content)
				return true
			}
			return false
		}
		_, err := NewClient(f.Client(), f.URL, "").ResolveVersion(context.Background(), "stable")
		if !errors.Is(err, ErrInvalidTag) {
			t.Errorf("%q: err = %v", content, err)
		}
	}
}

func TestResolveStableWithoutTagFile(t *testing.T) {
	f := newFake(t, []string{"v0.9.0"})
	f.status = func(r *http.Request, w http.ResponseWriter) bool {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			fmt.Fprint(w, `{"tag_name":"v0.9.0","assets":[]}`)
			return true
		}
		return false
	}
	_, err := NewClient(f.Client(), f.URL, "").ResolveVersion(context.Background(), "stable")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestResolveStableWhenLatestIsNotStable(t *testing.T) {
	f := newFake(t, []string{"b2", "v0.9.0", "b12110"})
	f.status = func(r *http.Request, w http.ResponseWriter) bool {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			fmt.Fprint(w, `{"tag_name":"b2","assets":[]}`)
			return true
		}
		return false
	}
	rel, err := NewClient(f.Client(), f.URL, "").ResolveVersion(context.Background(), "stable")
	if err != nil || rel.Tag != "v0.9.0" {
		t.Errorf("release = %+v, %v", rel, err)
	}
}

func TestCompare(t *testing.T) {
	f := newFake(t)
	c := NewClient(f.Client(), f.URL, "")
	got, err := c.Compare(context.Background(), "v0.9.0", "b12137")
	if err != nil {
		t.Fatal(err)
	}
	if got.AheadBy != 300 || len(got.Commits) != 2 || got.Commits[0].Title != "server : fix a thing (#1)" || got.Commits[1].SHA != "def" {
		t.Errorf("comparison = %+v", got)
	}
	if !strings.HasSuffix(f.requests[0].URL.Path, "/compare/v0.9.0...b12137") {
		t.Errorf("path = %s", f.requests[0].URL.Path)
	}
	if _, err := c.Compare(context.Background(), "../x", "b1"); !errors.Is(err, ErrInvalidTag) {
		t.Errorf("err = %v", err)
	}
	if _, err := c.Compare(context.Background(), "b1", "../x"); !errors.Is(err, ErrInvalidTag) {
		t.Errorf("err = %v", err)
	}
}

func TestTokenOnlyOnAPI(t *testing.T) {
	f := newFake(t, []string{"v0.9.0", "b12110"})
	if _, err := NewClient(f.Client(), f.URL, "secret").ResolveVersion(context.Background(), "stable"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests {
		got := r.Header.Get("Authorization")
		if strings.HasSuffix(r.URL.Path, nightlyAsset) {
			if got != "" {
				t.Errorf("%s got a token", r.URL.Path)
			}
		} else if got != "Bearer secret" {
			t.Errorf("%s: Authorization = %q", r.URL.Path, got)
		}
	}
	f.requests = nil
	if _, err := NewClient(f.Client(), f.URL, "").List(context.Background(), ChannelNightly, 1); err != nil {
		t.Fatal(err)
	}
	if got := f.requests[0].Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestCacheAndRevalidation(t *testing.T) {
	f := newFake(t, []string{"b1"})
	f.status = func(r *http.Request, w http.ResponseWriter) bool {
		if r.Header.Get("If-None-Match") == `"e1"` {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
		w.Header().Set("ETag", `"e1"`)
		return false
	}
	now := time.Unix(1000, 0)
	c := NewClient(f.Client(), f.URL, "")
	c.now = func() time.Time { return now }
	ctx := context.Background()

	for range 2 {
		if got, err := c.List(ctx, ChannelNightly, 1); err != nil || tags(got) != "b1" {
			t.Fatalf("got %q, %v", tags(got), err)
		}
	}
	if f.count() != 1 {
		t.Fatalf("requests = %d", f.count())
	}
	now = now.Add(cacheTTL + time.Second)
	if got, err := c.List(ctx, ChannelNightly, 1); err != nil || tags(got) != "b1" {
		t.Fatalf("revalidated got %q, %v", tags(got), err)
	}
	if f.count() != 2 || f.requests[1].Header.Get("If-None-Match") != `"e1"` {
		t.Errorf("requests = %d", f.count())
	}
	if got, err := c.List(ctx, ChannelNightly, 1); err != nil || tags(got) != "b1" || f.count() != 2 {
		t.Errorf("not fresh after 304: %q, %v, %d", tags(got), err, f.count())
	}
}

func TestRateLimitServesStale(t *testing.T) {
	f := newFake(t, []string{"b1"})
	limited := false
	reset := time.Unix(5000, 0)
	f.status = func(_ *http.Request, w http.ResponseWriter) bool {
		if !limited {
			return false
		}
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	now := time.Unix(1000, 0)
	c := NewClient(f.Client(), f.URL, "")
	c.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.List(ctx, ChannelNightly, 1); err != nil {
		t.Fatal(err)
	}

	limited = true
	now = now.Add(cacheTTL + time.Second)
	got, err := c.List(ctx, ChannelNightly, 1)
	if err != nil || tags(got) != "b1" {
		t.Fatalf("got %q, %v", tags(got), err)
	}

	_, err = c.Compare(ctx, "b1", "b2")
	var rl *RateLimitError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &rl) || !rl.Reset.Equal(reset) {
		t.Fatalf("err = %v", err)
	}

	// While the limit lasts nothing is sent at all.
	n := f.count()
	if _, err := c.Compare(ctx, "b1", "b3"); !errors.Is(err, ErrRateLimited) || f.count() != n {
		t.Errorf("err = %v, requests %d -> %d", err, n, f.count())
	}
	now = reset.Add(time.Second)
	limited = false
	if _, err := c.Compare(ctx, "b1", "b3"); err != nil || f.count() != n+1 {
		t.Errorf("after reset: %v", err)
	}
}

func TestRateLimitDetection(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		headers map[string]string
		body    string
		limited bool
	}{
		"429":         {429, nil, "", true},
		"retry after": {403, map[string]string{"Retry-After": "30"}, "", true},
		"secondary":   {403, nil, `{"message":"You have exceeded a secondary rate limit"}`, true},
		"forbidden":   {403, nil, `{"message":"Resource not accessible"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.status = func(_ *http.Request, w http.ResponseWriter) bool {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
				return true
			}
			now := time.Unix(1000, 0)
			c := NewClient(f.Client(), f.URL, "")
			c.now = func() time.Time { return now }
			_, err := c.List(context.Background(), ChannelNightly, 1)
			if errors.Is(err, ErrRateLimited) != tc.limited {
				t.Fatalf("err = %v", err)
			}
			var rl *RateLimitError
			if name == "retry after" && (!errors.As(err, &rl) || !rl.Reset.Equal(now.Add(30*time.Second))) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestCacheIsBounded(t *testing.T) {
	c := NewClient(nil, "http://x", "")
	for i := range maxCached + 10 {
		c.store(strconv.Itoa(i), &cacheEntry{value: i, fetched: time.Unix(int64(i), 0)})
	}
	if len(c.cache) != maxCached {
		t.Errorf("entries = %d", len(c.cache))
	}
	if _, ok := c.cache["0"]; ok {
		t.Error("oldest entry survived")
	}
}
