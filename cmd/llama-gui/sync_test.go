package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/profiles"
)

// profileAPI is a stateful stand-in for the profile, library and model routes. It
// stores profiles as the service does, through profiles.Profile, and records every
// call that changes something.
type profileAPI struct {
	mu       sync.Mutex
	profiles map[string]profiles.Profile
	local    map[string]*localModel   // by path
	repos    map[string][]repoVariant // what Hugging Face has, by repo
	offline  bool                     // Hugging Face cannot be reached
	writes   []string
	srv      *httptest.Server
}

func newProfileAPI(t *testing.T) *profileAPI {
	t.Helper()
	f := &profileAPI{
		profiles: map[string]profiles.Profile{},
		local:    map[string]*localModel{},
		repos: map[string][]repoVariant{
			"u/small": {
				{Quant: "Q8_0", DownloadFiles: []string{"small-Q8_0.gguf"}},
				{Quant: "Q4_K_M", DownloadFiles: []string{"small-Q4_K_M.gguf"}},
				{Quant: "Q4_K_M", Companion: true, DownloadFiles: []string{"small-draft-Q4_K_M.gguf"}},
			},
			// Split in two, with a vision projector: it downloads into a directory.
			"u/big": {{Quant: "UD-Q4_K_XL", DownloadFiles: []string{
				"UD-Q4_K_XL/big-UD-Q4_K_XL-00001-of-00002.gguf", "UD-Q4_K_XL/big-UD-Q4_K_XL-00002-of-00002.gguf", "mmproj-F16.gguf",
			}}},
		},
	}
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	decode := func(r *http.Request) (profiles.Profile, bool) {
		var p profiles.Profile
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		return p, dec.Decode(&p) == nil
	}
	notFound := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}
	mux.HandleFunc("GET /api/v1/profiles", func(w http.ResponseWriter, _ *http.Request) {
		list := []profiles.Profile{}
		for _, p := range f.profiles {
			list = append(list, p)
		}
		write(w, map[string]any{"profiles": list})
	})
	mux.HandleFunc("GET /api/v1/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := f.profiles[r.PathValue("id")]
		if !ok {
			notFound(w)
			return
		}
		write(w, p)
	})
	mux.HandleFunc("POST /api/v1/profiles", func(w http.ResponseWriter, r *http.Request) {
		p, ok := decode(r)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.profiles[p.ID] = p
		f.writes = append(f.writes, "create "+p.ID)
		w.WriteHeader(http.StatusCreated)
		write(w, p)
	})
	mux.HandleFunc("PUT /api/v1/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, ok := decode(r)
		if _, exists := f.profiles[id]; !ok || !exists {
			notFound(w)
			return
		}
		p.ID = id
		f.profiles[id] = p
		f.writes = append(f.writes, "update "+id)
		write(w, p)
	})
	mux.HandleFunc("DELETE /api/v1/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		delete(f.profiles, r.PathValue("id"))
		f.writes = append(f.writes, "delete "+r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/library/local", func(w http.ResponseWriter, _ *http.Request) {
		models := []localModel{}
		for _, m := range f.local {
			l := *m
			for _, p := range f.profiles {
				if localFor(f.localSnapshot(), p.ModelFile).Path == l.Path {
					l.RouterID = p.Alias
				}
			}
			models = append(models, l)
		}
		write(w, map[string]any{"models": models})
	})
	mux.HandleFunc("GET /api/v1/library/repo", func(w http.ResponseWriter, r *http.Request) {
		if f.offline {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"huggingface.co: no route to host"}`))
			return
		}
		variants, ok := f.repos[r.URL.Query().Get("repo")]
		if !ok {
			notFound(w)
			return
		}
		out := slices.Clone(variants)
		for i, v := range out {
			first := path.Base(v.DownloadFiles[0])
			for _, m := range f.local {
				if slices.Contains(m.Files, first) {
					out[i].LocalPath = m.Path
				}
			}
		}
		write(w, map[string]any{"repo": map[string]any{"variants": out}})
	})
	// A download lands as the service's queue lays it out: one file in the root, or
	// several in a directory named for the first.
	mux.HandleFunc("POST /api/v1/library/downloads", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Repo  string   `json:"repo"`
			Files []string `json:"files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m := &localModel{Status: "unloaded"}
		for _, file := range body.Files {
			base := path.Base(file)
			m.Files = append(m.Files, base)
			if strings.HasPrefix(base, "mmproj") {
				m.MMProj = base
			}
		}
		m.Path = m.Files[0]
		if len(m.Files) > 1 {
			m.Path = strings.TrimSuffix(strings.TrimSuffix(m.Files[0], ".gguf"), "-00001-of-00002")
		}
		f.local[m.Path] = m
		f.writes = append(f.writes, "pull "+body.Repo+" "+strings.Join(body.Files, ","))
		w.WriteHeader(http.StatusAccepted)
		write(w, map[string]string{"id": "dl-1", "state": "queued"})
	})
	mux.HandleFunc("GET /api/v1/library/downloads", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"jobs": []map[string]string{{"id": "dl-1", "state": "done"}}})
	})
	mux.HandleFunc("POST /api/v1/models/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		f.writes = append(f.writes, r.PathValue("action")+" "+r.PathValue("id"))
		write(w, map[string]string{"id": r.PathValue("id")})
	})
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// localSnapshot is f.local by value, for localFor.
func (f *profileAPI) localSnapshot() map[string]localModel {
	m := make(map[string]localModel, len(f.local))
	for k, v := range f.local {
		m[k] = *v
	}
	return m
}

func (f *profileAPI) setStatus(path, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.local[path].Status = status
}

// sync runs profiles sync over a manifest and returns what it changed.
func (f *profileAPI) sync(t *testing.T, manifest string) (int, string, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.writes = nil
	f.mu.Unlock()
	var out, errOut bytes.Buffer
	code := run([]string{"profiles", "sync", "--prefix", "nixos-", path, "--addr", strings.TrimPrefix(f.srv.URL, "http://")}, noEnv, &out, &errOut)
	f.mu.Lock()
	defer f.mu.Unlock()
	writes := slices.Clone(f.writes)
	slices.Sort(writes)
	return code, out.String() + errOut.String(), writes
}

// Zero values the service drops (parallel, an empty extra_args) and ones it keeps
// (jinja, temp), which a rerun must not mistake for a change. local is a file put
// in the models directory some other way.
const smallManifest = `[
	{"model": "hf.co/u/small:q4_k_m", "profile": {"id": "nixos-small", "name": "small", "alias": "small",
		"ctx_size": 4096, "parallel": 0, "extra_args": [], "jinja": false, "temp": 0}},
	{"model": "https://huggingface.co/u/big:UD-Q4_K_XL", "profile": {"id": "nixos-big", "name": "big",
		"alias": "big", "ctx_size": 8192}},
	{"profile": {"id": "nixos-local", "name": "local", "alias": "local",
		"model_file": "local.gguf", "ctx_size": 2048}}
]`

func fastPolls(t *testing.T) {
	saved := jobPoll
	jobPoll = time.Millisecond
	t.Cleanup(func() { jobPoll = saved })
}

func TestProfilesSyncIsIdempotent(t *testing.T) {
	fastPolls(t)
	f := newProfileAPI(t)
	f.profiles["mine"] = profiles.Profile{ID: "mine", Name: "mine", Alias: "mine", ModelFile: "x.gguf", CtxSize: 1}

	code, out, writes := f.sync(t, smallManifest)
	want := []string{
		"create nixos-big", "create nixos-local", "create nixos-small",
		"pull u/big UD-Q4_K_XL/big-UD-Q4_K_XL-00001-of-00002.gguf,UD-Q4_K_XL/big-UD-Q4_K_XL-00002-of-00002.gguf,mmproj-F16.gguf",
		"pull u/small small-Q4_K_M.gguf",
	}
	if code != 0 || !slices.Equal(writes, want) {
		t.Fatalf("first run: %d %q %q", code, out, writes)
	}
	small, big := f.profiles["nixos-small"], f.profiles["nixos-big"]
	if small.ModelFile != "small-Q4_K_M.gguf" || small.MMProj != "" {
		t.Errorf("small resolved to %q %q", small.ModelFile, small.MMProj)
	}
	if small.Jinja == nil || *small.Jinja || small.Temp == nil || *small.Temp != 0 {
		t.Errorf("pointer fields lost: %+v", small)
	}
	if big.ModelFile != "big-UD-Q4_K_XL/big-UD-Q4_K_XL-00001-of-00002.gguf" || big.MMProj != "big-UD-Q4_K_XL/mmproj-F16.gguf" {
		t.Errorf("big resolved to %q %q", big.ModelFile, big.MMProj)
	}

	for range 2 {
		code, out, writes = f.sync(t, smallManifest)
		if code != 0 || len(writes) != 0 || strings.Count(out, "up to date") != 3 {
			t.Fatalf("rerun: %d %q %q", code, out, writes)
		}
	}
	if _, ok := f.profiles["mine"]; !ok {
		t.Error("deleted a profile outside the prefix")
	}

	// Without Hugging Face, a profile whose files are on disk stays as it is.
	f.offline = true
	code, out, writes = f.sync(t, smallManifest)
	if code != 0 || len(writes) != 0 || !strings.Contains(out, "keeping small-Q4_K_M.gguf") {
		t.Fatalf("offline: %d %q %q", code, out, writes)
	}
}

func TestProfilesSyncOfflineWithNothingToKeep(t *testing.T) {
	f := newProfileAPI(t)
	f.offline = true
	code, out, writes := f.sync(t, `[{"model": "u/small:Q4_K_M", "profile": {"id": "nixos-small",
		"name": "small", "alias": "small", "ctx_size": 1}}]`)
	if code != 1 || len(writes) != 0 || !strings.Contains(out, "no route to host") {
		t.Fatalf("%d %q %q", code, out, writes)
	}
}

func TestProfilesSyncUnknownQuant(t *testing.T) {
	f := newProfileAPI(t)
	code, out, writes := f.sync(t, `[{"model": "u/small:IQ1_S", "profile": {"id": "nixos-small",
		"name": "small", "alias": "small", "ctx_size": 1}}]`)
	if code != 1 || len(writes) != 0 || !strings.Contains(out, "only Q8_0, Q4_K_M") {
		t.Fatalf("%d %q %q", code, out, writes)
	}
}

func TestProfilesSyncUpdatesReloadsAndDeletes(t *testing.T) {
	fastPolls(t)
	f := newProfileAPI(t)
	if code, out, _ := f.sync(t, smallManifest); code != 0 {
		t.Fatalf("%d %q", code, out)
	}
	f.setStatus("small-Q4_K_M.gguf", "loaded")
	f.profiles["mine"] = profiles.Profile{ID: "mine", Name: "mine", Alias: "mine", ModelFile: "x.gguf", CtxSize: 1}

	// small changes and is loaded, so it is reloaded; big and local go.
	changed := `[{"model": "u/small:Q4_K_M", "profile": {"id": "nixos-small", "name": "small", "alias": "small",
		"ctx_size": 8192}}]`
	code, out, writes := f.sync(t, changed)
	want := []string{"delete nixos-big", "delete nixos-local", "load small", "unload small", "update nixos-small"}
	if code != 0 || !slices.Equal(writes, want) {
		t.Fatalf("%d %q %q", code, out, writes)
	}
	if f.profiles["nixos-small"].CtxSize != 8192 || f.profiles["nixos-small"].Jinja != nil {
		t.Errorf("not replaced: %+v", f.profiles["nixos-small"])
	}
	if _, ok := f.profiles["mine"]; !ok {
		t.Error("deleted a profile outside the prefix")
	}
	if code, out, writes := f.sync(t, changed); code != 0 || len(writes) != 0 {
		t.Fatalf("rerun: %d %q %q", code, out, writes)
	}

	// Another quantisation is another download, and a profile that is not loaded
	// loads nothing.
	f.setStatus("small-Q4_K_M.gguf", "unloaded")
	code, out, writes = f.sync(t, strings.Replace(changed, "Q4_K_M", "Q8_0", 1))
	if code != 0 || !slices.Equal(writes, []string{"pull u/small small-Q8_0.gguf", "update nixos-small"}) {
		t.Fatalf("%d %q %q", code, out, writes)
	}
	if f.profiles["nixos-small"].ModelFile != "small-Q8_0.gguf" {
		t.Errorf("not switched: %+v", f.profiles["nixos-small"])
	}

	// An empty manifest removes every profile it owns.
	code, out, writes = f.sync(t, `[]`)
	if code != 0 || !slices.Equal(writes, []string{"delete nixos-small"}) || len(f.profiles) != 1 {
		t.Fatalf("%d %q %q %v", code, out, writes, f.profiles)
	}
}

func TestProfilesSyncLoadsAtStart(t *testing.T) {
	fastPolls(t)
	f := newProfileAPI(t)
	manifest := `[{"model": "u/small:Q4_K_M", "profile": {"id": "nixos-small", "name": "small", "alias": "small",
		"ctx_size": 4096, "load_at_start": true}}]`

	// The model is downloaded after the router started, so its load-on-startup
	// came too late and the sync loads it.
	code, out, writes := f.sync(t, manifest)
	want := []string{"create nixos-small", "load small", "pull u/small small-Q4_K_M.gguf"}
	if code != 0 || !slices.Equal(writes, want) || !strings.Contains(out, "small: loaded") {
		t.Fatalf("first run: %d %q %q", code, out, writes)
	}

	// Loaded, or loading on its own at the router's start, it is left alone.
	for _, status := range []string{"loaded", "loading"} {
		f.setStatus("small-Q4_K_M.gguf", status)
		if code, out, writes := f.sync(t, manifest); code != 0 || len(writes) != 0 {
			t.Fatalf("%s: %d %q %q", status, code, out, writes)
		}
	}

	// A change to a loaded profile reloads it once, without a second load.
	f.setStatus("small-Q4_K_M.gguf", "loaded")
	changed := strings.Replace(manifest, "4096", "8192", 1)
	code, out, writes = f.sync(t, changed)
	want = []string{"load small", "unload small", "update nixos-small"}
	if code != 0 || !slices.Equal(writes, want) {
		t.Fatalf("changed: %d %q %q", code, out, writes)
	}

	// Unloaded by then, as when load-on-startup is turned on, it is loaded.
	f.setStatus("small-Q4_K_M.gguf", "unloaded")
	code, out, writes = f.sync(t, manifest)
	if code != 0 || !slices.Equal(writes, []string{"load small", "update nixos-small"}) {
		t.Fatalf("unloaded: %d %q %q", code, out, writes)
	}

	// Without load_at_start, an unloaded model stays so.
	code, out, writes = f.sync(t, strings.Replace(manifest, `, "load_at_start": true`, "", 1))
	if code != 0 || !slices.Equal(writes, []string{"update nixos-small"}) {
		t.Fatalf("no load_at_start: %d %q %q", code, out, writes)
	}
}

func TestProfilesSyncRejectsBadManifests(t *testing.T) {
	f := newProfileAPI(t)
	for name, manifest := range map[string]string{
		"outside the prefix": `[{"profile": {"id": "other", "name": "a", "alias": "a", "model_file": "a.gguf", "ctx_size": 1}}]`,
		"the bare prefix":    `[{"profile": {"id": "nixos-", "name": "a", "alias": "a", "model_file": "a.gguf", "ctx_size": 1}}]`,
		"twice": `[{"profile": {"id": "nixos-a", "name": "a", "alias": "a", "model_file": "a.gguf", "ctx_size": 1}},
			{"profile": {"id": "nixos-a", "name": "b", "alias": "b", "model_file": "b.gguf", "ctx_size": 1}}]`,
		"model and file":   `[{"model": "u/a:Q4_K_M", "profile": {"id": "nixos-a", "name": "a", "alias": "a", "model_file": "a.gguf", "ctx_size": 1}}]`,
		"neither":          `[{"profile": {"id": "nixos-a", "name": "a", "alias": "a", "ctx_size": 1}}]`,
		"no quant":         `[{"model": "hf.co/u/a", "profile": {"id": "nixos-a", "name": "a", "alias": "a", "ctx_size": 1}}]`,
		"a bad repo":       `[{"model": "hf.co/u/../a:Q4_K_M", "profile": {"id": "nixos-a", "name": "a", "alias": "a", "ctx_size": 1}}]`,
		"an unknown field": `[{"profile": {"id": "nixos-a", "ctx": 1}}]`,
		"not a list":       `{}`,
	} {
		if code, out, writes := f.sync(t, manifest); code != 1 || len(writes) != 0 {
			t.Errorf("%s: %d %q %q", name, code, out, writes)
		}
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"profiles", "sync", "m.json"}, noEnv, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "--prefix") {
		t.Errorf("no prefix: %d %q", code, errOut.String())
	}
	if code := run([]string{"profiles", "nope"}, noEnv, &out, &errOut); code != 2 {
		t.Errorf("unknown subcommand: %d", code)
	}
}
