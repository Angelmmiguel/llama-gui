package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/library"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
)

const gib = int64(1) << 30

func gpuMachine(h *harness) {
	vram := uint64(24 * gib)
	h.hw = hardware.Snapshot{
		GPUs:   []hardware.GPU{{MemoryTotal: &vram}, {MemoryTotal: &vram}, {}},
		Memory: hardware.Memory{Total: uint64(64 * gib)},
	}
}

func repoInfo(repo string) library.RepoInfo {
	return library.RepoInfo{
		SearchResult: library.SearchResult{Repo: repo, Downloads: 7},
		Variants: []library.Variant{
			{ID: "m-Q4_K_M", Quant: "Q4_K_M", SizeBytes: 10 * gib, Shards: 1, Files: []library.File{{Name: "m-Q4_K_M.gguf", Size: 10 * gib}}},
			{ID: "m-Q8_0", Quant: "Q8_0", SizeBytes: 20 * gib, Shards: 1, Files: []library.File{{Name: "m-Q8_0.gguf", Size: 20 * gib}}},
			{ID: "m-F32", Quant: "F32", SizeBytes: 90 * gib, Shards: 1, Files: []library.File{{Name: "m-F32.gguf", Size: 90 * gib}}},
		},
		MMProj: []library.File{{Name: "mmproj-f16.gguf", Size: gib}, {Name: "mmproj-Q8_0.gguf", Size: gib / 2}},
	}
}

func metaFixture() library.Metadata {
	return library.Metadata{BlockCount: 32, HeadCount: 8, HeadCountKV: 8, EmbeddingLength: 2048, ParamCount: 7_000_000_000}
}

// sparseModel creates a file of a size without writing it. It is not a real
// header, which Scan tolerates and estimates around.
func sparseModel(t *testing.T, dir, rel string, size int64) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

func asList(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := body[key].([]any)
	if !ok {
		t.Fatalf("%s: %v", key, body)
	}
	out := make([]map[string]any, len(raw))
	for i, v := range raw {
		out[i] = v.(map[string]any)
	}
	return out
}

func TestLibraryLocal(t *testing.T) {
	h := newHarness(t)
	gpuMachine(h)
	sparseModel(t, h.models, "a-Q4_K_M.gguf", gib)
	sparseModel(t, h.models, "b-Q8_0.gguf", 2*gib)
	sparseModel(t, h.models, "vision/v-Q8_0.gguf", gib)
	h.profiles.items["1"] = profiles.Profile{ID: "1", Alias: "a", ModelFile: "a-Q4_K_M.gguf", CtxSize: 65536, Parallel: 4}
	h.profiles.items["2"] = profiles.Profile{ID: "2", Alias: "v", ModelFile: "vision/v-Q8_0.gguf", CtxSize: 8192}
	h.router.models = []llama.Model{
		{ID: "a", Status: "loaded", Memory: llama.Memory{Effective: llama.MemoryEstimate{Devices: []llama.Device{
			{Model: 3 * gib, Context: gib, Compute: gib}, {Model: gib},
		}}}},
		{ID: "v", Status: "unloaded"},
	}

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/library/local?ctx=8192&kv=q8_0&parallel=2", "")
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, body)
	}
	hw := body["hardware"].(map[string]any)
	if hw["vram_bytes"] != float64(48*gib) || hw["ram_bytes"] != float64(64*gib) || hw["headroom_bytes"] != float64(2*gib) {
		t.Errorf("hardware %v", hw)
	}
	models := asList(t, body, "models")
	if len(models) != 3 {
		t.Fatalf("%v", models)
	}
	loaded := models[0]
	if loaded["status"] != "loaded" || loaded["router_id"] != "a" {
		t.Errorf("loaded %v", loaded)
	}
	if fit := loaded["fit"].(map[string]any); fit["calibrated"] != true || fit["weights_bytes"] != float64(4*gib) {
		t.Errorf("calibrated fit %v", fit)
	}
	if prof := loaded["profile"].(map[string]any); prof["alias"] != "a" || prof["ctx_size"] != float64(65536) {
		t.Errorf("profile %v", prof)
	}
	noProfile := models[1]
	if noProfile["profile"] != nil || noProfile["status"] != "unloaded" || noProfile["router_id"] != nil {
		t.Errorf("no profile %v", noProfile)
	}
	if fit := noProfile["fit"].(map[string]any); fit["calibrated"] != false || fit["verdict"] != library.VerdictFitsGPU {
		t.Errorf("fit %v", fit)
	}
	if dir := models[2]; dir["router_id"] != "v" || dir["status"] != "unloaded" {
		t.Errorf("directory model %v", dir)
	}

	h.table(t, []route{
		{"bad ctx", http.MethodGet, "/api/v1/library/local?ctx=abc", "", http.StatusBadRequest, "ctx"},
		{"ctx too small", http.MethodGet, "/api/v1/library/local?ctx=1", "", http.StatusBadRequest, "ctx"},
		{"bad parallel", http.MethodGet, "/api/v1/library/local?parallel=0", "", http.StatusBadRequest, "parallel"},
		{"bad kv", http.MethodGet, "/api/v1/library/local?kv=q9", "", http.StatusBadRequest, "kv"},
	})
}

func TestLibraryLocalWithoutTheRouter(t *testing.T) {
	h := newHarness(t)
	sparseModel(t, h.models, "a.gguf", gib)
	for _, setup := range []func(){func() { h.up = false }, func() { h.up, h.router.err = true, errors.New("boom") }} {
		setup()
		code, body := h.sendJSON(t, http.MethodGet, "/api/v1/library/local", "")
		if models := asList(t, body, "models"); code != http.StatusOK || len(models) != 1 || models[0]["status"] != "unloaded" {
			t.Errorf("%d %v", code, body)
		}
	}
}

func TestLibraryLocalHostOnly(t *testing.T) {
	h := newHarness(t)
	h.hw.Memory.Total = uint64(16 * gib)
	sparseModel(t, h.models, "a.gguf", gib)
	sparseModel(t, h.models, "huge.gguf", 40*gib)
	_, body := h.sendJSON(t, http.MethodGet, "/api/v1/library/local?ctx=4096", "")
	models := asList(t, body, "models")
	if got := models[0]["fit"].(map[string]any)["verdict"]; got != library.VerdictFitsRAM {
		t.Errorf("verdict %v", got)
	}
	if got := models[1]["fit"].(map[string]any)["verdict"]; got != library.VerdictTooLarge {
		t.Errorf("verdict %v", got)
	}
}

func TestLibraryDelete(t *testing.T) {
	h := newHarness(t)
	sparseModel(t, h.models, "gone.gguf", 8)
	sparseModel(t, h.models, "busy.gguf", 8)
	sparseModel(t, h.models, "notes.txt", 8)
	outside := filepath.Join(t.TempDir(), "x.gguf")
	sparseModel(t, filepath.Dir(outside), "x.gguf", 8)
	h.profiles.items["1"] = profiles.Profile{ID: "1", Alias: "busy", ModelFile: "busy.gguf"}
	h.router.models = []llama.Model{{ID: "busy", Status: "loaded"}}
	h.table(t, []route{
		{"missing path", http.MethodDelete, "/api/v1/library/local", "", http.StatusBadRequest, "path"},
		{"control character", http.MethodDelete, "/api/v1/library/local?path=a%0Ab.gguf", "", http.StatusBadRequest, "path"},
		{"traversal", http.MethodDelete, "/api/v1/library/local?path=../x.gguf", "", http.StatusBadRequest, "invalid model path"},
		{"absolute", http.MethodDelete, "/api/v1/library/local?path=" + outside, "", http.StatusBadRequest, "invalid model path"},
		{"not a model", http.MethodDelete, "/api/v1/library/local?path=notes.txt", "", http.StatusBadRequest, "GGUF"},
		{"unknown", http.MethodDelete, "/api/v1/library/local?path=none.gguf", "", http.StatusNotFound, ""},
		{"loaded", http.MethodDelete, "/api/v1/library/local?path=busy.gguf", "", http.StatusConflict, "unload busy"},
	})
	if h.reloads != 0 {
		t.Errorf("reloaded after failures: %d", h.reloads)
	}
	h.table(t, []route{{"deletes", http.MethodDelete, "/api/v1/library/local?path=gone.gguf", "", http.StatusOK, ""}})
	if _, err := os.Stat(filepath.Join(h.models, "gone.gguf")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("still there: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside file: %v", err)
	}
	if h.reloads != 1 {
		t.Errorf("reloads %d", h.reloads)
	}
	if w := h.send(t, http.MethodDelete, "/api/v1/library/local?path=busy.gguf", "", "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusForbidden {
		t.Errorf("cross-site %d", w.Code)
	}
}

func TestLibraryDisk(t *testing.T) {
	h := newHarness(t)
	sparseModel(t, h.models, "a.gguf", 1024)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/library/disk", "")
	if code != http.StatusOK || body["gguf_bytes"] != float64(1024) || body["path"] != h.models {
		t.Errorf("%d %v", code, body)
	}
	h.models = filepath.Join(h.models, "missing")
	h.handler = Handler(h.deps())
	h.table(t, []route{{"missing", http.MethodGet, "/api/v1/library/disk", "", http.StatusNotFound, ""}})
}

func TestLibraryRepo(t *testing.T) {
	h := newHarness(t)
	gpuMachine(h)
	h.hub.repos["u/m"] = repoInfo("u/m")
	h.hub.meta = metaFixture()

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/library/repo?repo=u/m&ctx=8192&kv=q4_0", "")
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, body)
	}
	repo := body["repo"].(map[string]any)
	if repo["name"] != "m" || repo["best"] != "m-Q8_0" || repo["metadata"] == nil {
		t.Errorf("repo %v", repo)
	}
	variants := asList(t, repo, "variants")
	if v := variants[1]; v["fit"].(map[string]any)["verdict"] != library.VerdictFitsGPU ||
		fmt.Sprint(v["download_files"]) != "[m-Q8_0.gguf mmproj-Q8_0.gguf]" || v["download_bytes"] != float64(20*gib+gib/2) {
		t.Errorf("variant %v", v)
	}
	if v := variants[0]; fmt.Sprint(v["download_files"]) != "[m-Q4_K_M.gguf mmproj-f16.gguf]" {
		t.Errorf("fallback mmproj %v", v)
	}
	if got := variants[2]["fit"].(map[string]any)["verdict"]; got != library.VerdictPartialOffload {
		t.Errorf("verdict %v", got)
	}
	if !slices.Contains(h.hub.calls, "meta m-Q4_K_M.gguf") {
		t.Errorf("calls %v", h.hub.calls)
	}

	// A header that can't be read degrades the estimate, not the answer.
	h.hub.metaErr = library.ErrUnauthorized
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/library/repo?repo=u/m", "")
	repo = body["repo"].(map[string]any)
	if !strings.Contains(repo["metadata_error"].(string), "token") ||
		asList(t, repo, "variants")[0]["fit"].(map[string]any)["estimated"] != true {
		t.Errorf("repo %v", repo)
	}

	h.table(t, []route{
		{"unknown", http.MethodGet, "/api/v1/library/repo?repo=u/none", "", http.StatusNotFound, "not found"},
		{"no repo", http.MethodGet, "/api/v1/library/repo", "", http.StatusBadRequest, "repo"},
		{"traversal", http.MethodGet, "/api/v1/library/repo?repo=../etc", "", http.StatusBadRequest, "repo"},
		{"too deep", http.MethodGet, "/api/v1/library/repo?repo=a/b/c", "", http.StatusBadRequest, "repo"},
		{"bad kv", http.MethodGet, "/api/v1/library/repo?repo=u/m&kv=x", "", http.StatusBadRequest, "kv"},
	})
}

func TestLibrarySearch(t *testing.T) {
	h := newHarness(t)
	gpuMachine(h)
	h.hub.results = []library.SearchResult{{Repo: "u/plain", Downloads: 3}}
	h.hub.repos["u/m"] = repoInfo("u/m")
	h.hub.meta = metaFixture()
	first := library.Catalog()[0]
	h.hub.cands = []library.Candidate{
		{CatalogEntry: first, Variants: repoInfo("").Variants, Meta: metaFixture()},
		{CatalogEntry: library.CatalogEntry{Repo: "u/embed", Name: "embed", Tags: []string{"embedding"}}, Variants: repoInfo("").Variants},
	}

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/library/search?q=llama&filter=vision", "")
	rows := asList(t, body, "results")
	if code != http.StatusOK || body["source"] != "search" || len(rows) != 1 || rows[0]["name"] != "plain" || rows[0]["variants"] != nil {
		t.Errorf("%d %v", code, body)
	}
	if h.hub.calls[0] != "search llama image-text-to-text" {
		t.Errorf("calls %v", h.hub.calls)
	}

	for _, q := range []string{"u/m", "https://huggingface.co/u/m"} {
		code, body = h.sendJSON(t, http.MethodGet, "/api/v1/library/search?q="+q, "")
		rows = asList(t, body, "results")
		if code != http.StatusOK || body["source"] != "repo" || len(rows) != 1 || len(asList(t, rows[0], "variants")) != 3 {
			t.Errorf("%s: %d %v", q, code, body)
		}
	}

	code, body = h.sendJSON(t, http.MethodGet, "/api/v1/library/search", "")
	rows = asList(t, body, "results")
	if code != http.StatusOK || body["source"] != "catalog" || len(rows) != 2 || rows[0]["name"] != first.Name ||
		rows[0]["description"] != first.Description || rows[0]["catalog"] != true || rows[0]["best"] == nil {
		t.Errorf("%d %v", code, body)
	}
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/library/search?filter=embedding", "")
	if rows = asList(t, body, "results"); len(rows) != 1 || rows[0]["repo"] != "u/embed" {
		t.Errorf("filtered %v", rows)
	}

	h.table(t, []route{
		{"bad filter", http.MethodGet, "/api/v1/library/search?filter=nope", "", http.StatusBadRequest, "filter"},
		{"long query", http.MethodGet, "/api/v1/library/search?q=" + strings.Repeat("a", 201), "", http.StatusBadRequest, "q:"},
		{"control character", http.MethodGet, "/api/v1/library/search?q=a%00b", "", http.StatusBadRequest, "q:"},
		{"unknown repo", http.MethodGet, "/api/v1/library/search?q=u/none", "", http.StatusNotFound, "not found"},
	})
}

func TestLibraryHubFailures(t *testing.T) {
	const token = "hf_secret_token"
	cases := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"missing", library.ErrNotFound, http.StatusNotFound, "not found"},
		{"gated", library.ErrUnauthorized, http.StatusUnauthorized, "token may be required"},
		{"rate limited", library.ErrRateLimited, http.StatusTooManyRequests, "rate limit"},
		{"offline", errors.New("dial tcp: Authorization: Bearer " + token), http.StatusBadGateway, "could not reach"},
		{"hub error", fmt.Errorf("hub returned 500 Internal Server Error"), http.StatusBadGateway, "could not reach"},
	}
	paths := []string{
		"/api/v1/library/search?q=x", "/api/v1/library/search?q=u/m", "/api/v1/library/search",
		"/api/v1/library/repo?repo=u/m", "/api/v1/library/suggestions",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.hub.err = tc.err
			for _, p := range paths {
				w := h.send(t, http.MethodGet, p, "")
				if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) || strings.Contains(w.Body.String(), token) {
					t.Errorf("%s: %d %s", p, w.Code, w.Body)
				}
			}
		})
	}

	h := newHarness(t)
	h.hub.err = fmt.Errorf("%w (retry after 42 s)", library.ErrRateLimited)
	w := h.send(t, http.MethodGet, "/api/v1/library/search?q=x", "")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "42" {
		t.Errorf("%d %v", w.Code, w.Header())
	}
}

func TestLibrarySuggestions(t *testing.T) {
	h := newHarness(t)
	gpuMachine(h)
	info := repoInfo("u/m")
	h.hub.repos["u/m"] = info
	h.hub.cands = []library.Candidate{{
		CatalogEntry: library.CatalogEntry{Repo: "u/m", Name: "M", Tags: []string{"chat"}},
		Variants:     info.Variants, Meta: metaFixture(),
	}}
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/library/suggestions?ctx=8192&kv=q8_0", "")
	list := asList(t, body, "suggestions")
	if code != http.StatusOK || len(list) == 0 {
		t.Fatalf("%d %v", code, body)
	}
	first := list[0]
	if first["category"] != library.SuggestLargestDense || first["quant"] != "Q8_0" ||
		first["metadata"] == nil || fmt.Sprint(first["download_files"]) != "[m-Q8_0.gguf mmproj-Q8_0.gguf]" {
		t.Errorf("%v", first)
	}

	h.hub.cands = nil
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/library/suggestions", "")
	if list, ok := body["suggestions"].([]any); !ok || len(list) != 0 {
		t.Errorf("empty %v", body)
	}
	h.table(t, []route{
		{"bad ctx", http.MethodGet, "/api/v1/library/suggestions?ctx=x", "", http.StatusBadRequest, "ctx"},
		{"bad kv", http.MethodGet, "/api/v1/library/suggestions?kv=x", "", http.StatusBadRequest, "kv"},
	})
}

func TestLibraryDownloads(t *testing.T) {
	h := newHarness(t)
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/library/downloads", "")
	if jobs, ok := body["jobs"].([]any); code != http.StatusOK || !ok || len(jobs) != 0 {
		t.Errorf("%d %v", code, body)
	}
	h.downloads.jobs = []library.Job{{ID: "dl-1", State: library.JobDownloading, Done: 5, Total: 10}}
	if _, body = h.sendJSON(t, http.MethodGet, "/api/v1/library/downloads", ""); asList(t, body, "jobs")[0]["done"] != float64(5) {
		t.Errorf("%v", body)
	}

	h.table(t, []route{
		{"queues", http.MethodPost, "/api/v1/library/downloads", `{"repo":"u/m","files":["a.gguf","b.gguf"]}`, http.StatusAccepted, ""},
		{"bad repo", http.MethodPost, "/api/v1/library/downloads", `{"repo":"../m","files":["a.gguf"]}`, http.StatusBadRequest, "repo"},
		{"no files", http.MethodPost, "/api/v1/library/downloads", `{"repo":"u/m"}`, http.StatusBadRequest, "files"},
		{"unknown field", http.MethodPost, "/api/v1/library/downloads", `{"repo":"u/m","x":1}`, http.StatusBadRequest, "JSON"},
		{"empty", http.MethodPost, "/api/v1/library/downloads", "", http.StatusBadRequest, "JSON"},
		{"cancels", http.MethodDelete, "/api/v1/library/downloads/dl-1", "", http.StatusOK, ""},
		{"removes partials", http.MethodDelete, "/api/v1/library/downloads/dl-2?remove_partials=true", "", http.StatusOK, ""},
		{"bad flag", http.MethodDelete, "/api/v1/library/downloads/dl-2?remove_partials=x", "", http.StatusBadRequest, "remove_partials"},
		{"bad id", http.MethodDelete, "/api/v1/library/downloads/a%20b", "", http.StatusBadRequest, "id"},
	})
	want := []string{"enqueue u/m a.gguf,b.gguf", "cancel dl-1 false", "cancel dl-2 true"}
	if !slices.Equal(h.downloads.calls, want) {
		t.Errorf("calls %q", h.downloads.calls)
	}
	if w := h.send(t, http.MethodPost, "/api/v1/library/downloads", `{"repo":"u/m","files":["a.gguf"]}`, "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusForbidden {
		t.Errorf("cross-site %d", w.Code)
	}

	for _, tc := range []struct {
		err    error
		status int
	}{
		{library.ErrInvalidName, http.StatusBadRequest},
		{library.ErrNoModel, http.StatusBadRequest},
		{library.ErrNotFound, http.StatusNotFound},
		{library.ErrUnauthorized, http.StatusUnauthorized},
		{library.ErrExists, http.StatusConflict},
		{library.ErrDuplicate, http.StatusConflict},
		{library.ErrNoSpace, http.StatusInsufficientStorage},
		{library.ErrClosed, http.StatusServiceUnavailable},
	} {
		h.downloads.err = tc.err
		if code, body := h.sendJSON(t, http.MethodPost, "/api/v1/library/downloads", `{"repo":"u/m","files":["a.gguf"]}`); code != tc.status {
			t.Errorf("%v: %d %v", tc.err, code, body)
		}
	}
	h.downloads.err = library.ErrNoJob
	h.table(t, []route{{"unknown job", http.MethodDelete, "/api/v1/library/downloads/dl-9", "", http.StatusNotFound, "no such"}})
}
