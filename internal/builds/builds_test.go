package builds

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

const routerScript = `#!/bin/sh
case "$1" in
--version) echo 'version: 0.9.0-dev (build 12132, commit 2ada83079)' ;;
--help) printf -- '-t,    --threads N      threads\n--models-preset FILE   presets\n' ;;
--list-devices) printf 'Available devices:\n  Vulkan0: Some GPU\n' ;;
esac
`

const plainScript = `#!/bin/sh
case "$1" in
--version) echo 'version: 100 (abc1234)' ;;
--help) echo '-m, --model FNAME   model; see --models-preset' ;;
esac
`

type entry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	link     string
}

func tgz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: e.typeflag, Linkname: e.link, Size: int64(len(e.body))}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serverArchive(t *testing.T, tag, script string) []byte {
	top := "llama-" + tag + "/"
	return tgz(t,
		entry{name: top, typeflag: tar.TypeDir, mode: 0o755},
		entry{name: top + "llama-server", body: script, mode: 0o755},
		entry{name: top + "libllama.so.0.9.0", body: "lib", mode: 0o755},
		entry{name: top + "libllama.so.0", typeflag: tar.TypeSymlink, link: "libllama.so.0.9.0"},
		entry{name: top + "libllama.so", typeflag: tar.TypeSymlink, link: "libllama.so.0"},
		entry{name: top + "sub/hard", typeflag: tar.TypeLink, link: top + "libllama.so.0.9.0"},
		entry{name: top + "suid", body: "x", mode: 0o6755},
	)
}

// assetServer serves files with Range support, and remembers the Range headers it saw.
type assetServer struct {
	*httptest.Server
	mu     sync.Mutex
	files  map[string][]byte
	ranges []string
	hook   func(w http.ResponseWriter, r *http.Request) bool
}

func newAssetServer(t *testing.T) *assetServer {
	t.Helper()
	s := &assetServer{files: map[string][]byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.ranges = append(s.ranges, r.Header.Get("Range"))
		hook := s.hook
		body, ok := s.files[strings.TrimPrefix(r.URL.Path, "/")]
		s.mu.Unlock()
		if hook != nil && hook(w, r) {
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *assetServer) add(name string, body []byte) releases.Asset {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[name] = body
	sum := sha256.Sum256(body)
	return releases.Asset{Name: name, Size: int64(len(body)), URL: s.URL + "/" + name, Digest: "sha256:" + hex.EncodeToString(sum[:])}
}

type fakeReleases map[string]releases.Release

func (f fakeReleases) ResolveVersion(_ context.Context, version string) (releases.Release, error) {
	rel, ok := f[version]
	if !ok {
		return releases.Release{}, releases.ErrNotFound
	}
	return rel, nil
}

type memStore struct {
	mu sync.Mutex
	id string
}

func (s *memStore) ActiveBuild(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id, nil
}

func (s *memStore) SetActiveBuild(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
	return nil
}

type fixture struct {
	m     *Manager
	dir   string
	srv   *assetServer
	rels  fakeReleases
	store *memStore
	snap  hardware.Snapshot
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: filepath.Join(t.TempDir(), "builds"), srv: newAssetServer(t), rels: fakeReleases{}, store: &memStore{}}
	m, err := New(Options{
		Dir:      f.dir,
		Releases: f.rels,
		Hardware: func(context.Context) hardware.Snapshot { return f.snap },
		Store:    f.store,
		Arch:     ArchX64,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.m = m
	t.Cleanup(m.Close)
	return f
}

// release publishes tag with a CPU build running script.
func (f *fixture) release(t *testing.T, tag, script string) releases.Asset {
	t.Helper()
	a := f.srv.add("llama-"+tag+"-bin-ubuntu-x64.tar.gz", serverArchive(t, tag, script))
	f.rels[tag] = releases.Release{Tag: tag, BinaryTag: tag, Channel: releases.ChannelNightly, Assets: []releases.Asset{a}}
	return a
}

func (f *fixture) install(t *testing.T, version, backend string) Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	j, err := f.m.Install(ctx, version, backend)
	if err != nil {
		t.Fatalf("Install(%s, %s): %v", version, backend, err)
	}
	j, err = f.m.Wait(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (f *fixture) mustInstall(t *testing.T, tag string) {
	t.Helper()
	f.release(t, tag, routerScript)
	if j := f.install(t, tag, BackendCPU); j.State != StateDone {
		t.Fatalf("job = %+v", j)
	}
}

func (f *fixture) tmpEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.dir, tmpDirName))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestInstall(t *testing.T) {
	f := newFixture(t)
	a := f.release(t, "b12132", routerScript)
	j := f.install(t, "b12132", BackendCPU)
	if j.State != StateDone || j.ID != "b12132-cpu" || j.Done != a.Size || j.Total != a.Size || j.Error != "" || j.FinishedAt.IsZero() {
		t.Fatalf("job = %+v", j)
	}

	dir := filepath.Join(f.dir, j.ID)
	if got, _ := os.Readlink(filepath.Join(dir, "libllama.so")); got != "libllama.so.0" {
		t.Errorf("symlink = %q", got)
	}
	if !exists(filepath.Join(dir, "sub/hard")) {
		t.Error("hard link missing")
	}
	info, err := os.Stat(filepath.Join(dir, "suid"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&(fs.ModeSetuid|fs.ModeSetgid) != 0 {
		t.Errorf("suid mode = %v", info.Mode())
	}
	if len(f.tmpEntries(t)) != 0 {
		t.Errorf("tmp = %v", f.tmpEntries(t))
	}

	man, err := f.m.Get(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := man.Probe
	if p.Version != "0.9.0-dev" || p.Build != "12132" || p.Commit != "2ada83079" || !p.Router {
		t.Errorf("probe = %+v", p)
	}
	if !slices.Contains(p.Flags, "--threads") || !slices.Equal(p.Devices, []string{"Vulkan0: Some GPU"}) {
		t.Errorf("probe = %+v", p)
	}
	if man.Tag != "b12132" || man.ReleaseTag != "b12132" || man.Backend != BackendCPU || man.Arch != ArchX64 || man.Assets[0].Digest != a.Digest {
		t.Errorf("manifest = %+v", man)
	}

	bin, err := f.m.ServerBinary(j.ID)
	if err != nil || bin != filepath.Join(f.dir, j.ID, "llama-server") {
		t.Errorf("binary = %q, %v", bin, err)
	}
	if active, _ := f.m.Active(context.Background()); active != "" {
		t.Errorf("active = %q after install", active)
	}
	if _, err := f.m.Install(context.Background(), "b12132", BackendCPU); !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("reinstall err = %v", err)
	}
}

func TestInstallStableUsesBinaryTag(t *testing.T) {
	f := newFixture(t)
	a := f.release(t, "b12110", routerScript)
	f.rels["stable"] = releases.Release{Tag: "v0.9.0", BinaryTag: "b12110", Channel: releases.ChannelStable, Assets: []releases.Asset{a}}
	j := f.install(t, "stable", BackendCPU)
	if j.State != StateDone || j.ID != "b12110-cpu" {
		t.Fatalf("job = %+v", j)
	}
	man, err := f.m.Get(j.ID)
	if err != nil || man.ReleaseTag != "v0.9.0" || man.Channel != releases.ChannelStable {
		t.Errorf("manifest = %+v, %v", man, err)
	}
}

func TestInstallCUDAWithRuntime(t *testing.T) {
	f := newFixture(t)
	f.snap.CUDAVersion = "13.0"
	f.snap.GPUs = []hardware.GPU{{Name: "x"}}
	main := f.srv.add("llama-b1-bin-ubuntu-cuda-13.4-x64.tar.gz", serverArchive(t, "b1", routerScript))
	// As upstream: the runtime's top directory is named after the asset, not llama-<tag>.
	top := "cudart-llama-b1-bin-ubuntu-cuda-13.4-x64/"
	rt := f.srv.add(top[:len(top)-1]+".tar.gz", tgz(t,
		entry{name: top, typeflag: tar.TypeDir, mode: 0o755},
		entry{name: top + "libcudart.so.13", body: "rt"},
		entry{name: top + "libllama.so.0.9.0", body: "replaced"},
	))
	f.rels["b1"] = releases.Release{Tag: "b1", BinaryTag: "b1", Assets: []releases.Asset{main, rt}}
	j := f.install(t, "b1", BackendCUDA)
	if j.State != StateDone || j.Total != main.Size+rt.Size || j.Done != j.Total {
		t.Fatalf("job = %+v", j)
	}
	dir := filepath.Join(f.dir, "b1-cuda")
	if !exists(filepath.Join(dir, "libcudart.so.13")) || !exists(filepath.Join(dir, "llama-server")) {
		t.Error("files missing")
	}
	man, _ := f.m.Get("b1-cuda")
	if man.Variant != "13.4" || len(man.Assets) != 2 {
		t.Errorf("manifest = %+v", man)
	}
}

func TestInstallErrorsBeforeJob(t *testing.T) {
	f := newFixture(t)
	f.release(t, "b1", routerScript)
	ctx := context.Background()
	for _, tc := range []struct {
		version, backend string
		want             error
	}{
		{"../../etc", BackendCPU, releases.ErrInvalidTag},
		{"b1", "../x", ErrInvalidBackend},
		{"b404", BackendCPU, releases.ErrNotFound},
		{"b1", BackendVulkan, ErrNoAsset},
	} {
		if _, err := f.m.Install(ctx, tc.version, tc.backend); !errors.Is(err, tc.want) {
			t.Errorf("Install(%s, %s) = %v", tc.version, tc.backend, err)
		}
	}
	if len(f.m.Jobs()) != 0 {
		t.Errorf("jobs = %+v", f.m.Jobs())
	}

	f.rels["b2"] = releases.Release{Tag: "b2", BinaryTag: "../../evil"}
	if _, err := f.m.Install(ctx, "b2", BackendCPU); !errors.Is(err, releases.ErrInvalidTag) {
		t.Errorf("evil binary tag = %v", err)
	}
}

func TestResume(t *testing.T) {
	f := newFixture(t)
	a := f.release(t, "b1", routerScript)
	body := f.srv.files[a.Name]
	if err := os.MkdirAll(filepath.Join(f.dir, tmpDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(f.dir, tmpDirName, a.Name+".part")
	if err := os.WriteFile(part, body[:len(body)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if j := f.install(t, "b1", BackendCPU); j.State != StateDone || j.Done != a.Size {
		t.Fatalf("job = %+v", j)
	}
	if want := fmt.Sprintf("bytes=%d-", len(body)/2); !slices.Contains(f.srv.ranges, want) {
		t.Errorf("ranges = %q, want %q", f.srv.ranges, want)
	}
}

func TestResumeFallbacks(t *testing.T) {
	for name, hook := range map[string]func(w http.ResponseWriter, r *http.Request) bool{
		"server ignores Range": func(w http.ResponseWriter, r *http.Request) bool {
			r.Header.Del("Range")
			return false
		},
		"range not satisfiable": func(w http.ResponseWriter, r *http.Request) bool {
			if r.Header.Get("Range") != "" {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return true
			}
			return false
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.release(t, "b1", routerScript)
			f.srv.hook = hook
			if err := os.MkdirAll(filepath.Join(f.dir, tmpDirName), 0o755); err != nil {
				t.Fatal(err)
			}
			part := filepath.Join(f.dir, tmpDirName, a.Name+".part")
			if err := os.WriteFile(part, []byte("garbage"), 0o600); err != nil {
				t.Fatal(err)
			}
			if j := f.install(t, "b1", BackendCPU); j.State != StateDone || j.Done != a.Size {
				t.Fatalf("job = %+v", j)
			}
		})
	}
}

func TestCompletePartialIsNotDownloadedAgain(t *testing.T) {
	f := newFixture(t)
	a := f.release(t, "b1", routerScript)
	if err := os.MkdirAll(filepath.Join(f.dir, tmpDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(f.dir, tmpDirName, a.Name+".part")
	if err := os.WriteFile(part, f.srv.files[a.Name], 0o600); err != nil {
		t.Fatal(err)
	}
	if j := f.install(t, "b1", BackendCPU); j.State != StateDone {
		t.Fatalf("job = %+v", j)
	}
	if len(f.srv.ranges) != 0 {
		t.Errorf("requests = %q", f.srv.ranges)
	}
}

func TestDigestMismatchDeletesPartial(t *testing.T) {
	f := newFixture(t)
	a := f.release(t, "b1", routerScript)
	good := a.Digest
	rel := f.rels["b1"]
	rel.Assets = []releases.Asset{{Name: a.Name, Size: a.Size, URL: a.URL, Digest: "sha256:" + strings.Repeat("0", 64)}}
	f.rels["b1"] = rel

	j := f.install(t, "b1", BackendCPU)
	if j.State != StateFailed || !strings.Contains(j.Error, "digest mismatch") {
		t.Fatalf("job = %+v", j)
	}
	if exists(filepath.Join(f.dir, "b1-cpu")) || len(f.tmpEntries(t)) != 0 {
		t.Errorf("left behind: %v", f.tmpEntries(t))
	}

	rel.Assets[0].Digest = good
	if j := f.install(t, "b1", BackendCPU); j.State != StateDone {
		t.Fatalf("retry job = %+v", j)
	}
}

func TestNoDigestStillInstalls(t *testing.T) {
	f := newFixture(t)
	a := f.release(t, "b1", routerScript)
	a.Digest = ""
	f.rels["b1"] = releases.Release{Tag: "b1", BinaryTag: "b1", Assets: []releases.Asset{a}}
	if j := f.install(t, "b1", BackendCPU); j.State != StateDone {
		t.Fatalf("job = %+v", j)
	}
}

func TestDownloadFailures(t *testing.T) {
	t.Run("HTTP error", func(t *testing.T) {
		f := newFixture(t)
		f.release(t, "b1", routerScript)
		f.srv.hook = func(w http.ResponseWriter, _ *http.Request) bool {
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		if j := f.install(t, "b1", BackendCPU); j.State != StateFailed || !strings.Contains(j.Error, "HTTP 502") {
			t.Errorf("job = %+v", j)
		}
	})
	t.Run("more bytes than announced", func(t *testing.T) {
		f := newFixture(t)
		a := f.release(t, "b1", routerScript)
		a.Size = 5
		a.Digest = ""
		f.rels["b1"] = releases.Release{Tag: "b1", BinaryTag: "b1", Assets: []releases.Asset{a}}
		if j := f.install(t, "b1", BackendCPU); j.State != StateFailed || !strings.Contains(j.Error, "more than") {
			t.Errorf("job = %+v", j)
		}
		if len(f.tmpEntries(t)) != 0 {
			t.Errorf("tmp = %v", f.tmpEntries(t))
		}
	})
	t.Run("bad URL scheme", func(t *testing.T) {
		f := newFixture(t)
		a := f.release(t, "b1", routerScript)
		a.URL = "file:///etc/passwd"
		f.rels["b1"] = releases.Release{Tag: "b1", BinaryTag: "b1", Assets: []releases.Asset{a}}
		if j := f.install(t, "b1", BackendCPU); j.State != StateFailed || !strings.Contains(j.Error, "bad asset URL") {
			t.Errorf("job = %+v", j)
		}
	})
	t.Run("stalled", func(t *testing.T) {
		f := newFixture(t)
		f.m.stallTimeout = 50 * time.Millisecond
		f.release(t, "b1", routerScript)
		release := make(chan struct{})
		defer close(release)
		f.srv.hook = func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return true
		}
		if j := f.install(t, "b1", BackendCPU); j.State != StateFailed || !strings.Contains(j.Error, "stalled") {
			t.Errorf("job = %+v", j)
		}
	})
}

// blockAfterHalf makes the server send half of the body and hang until the client leaves.
func blockAfterHalf(body []byte, started chan<- struct{}) func(w http.ResponseWriter, r *http.Request) bool {
	var once sync.Once
	return func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		once.Do(func() { close(started) })
		<-r.Context().Done()
		return true
	}
}

func TestCancelKeepsPartialForResume(t *testing.T) {
	f := newFixture(t)
	a := f.release(t, "b1", routerScript)
	body := f.srv.files[a.Name]
	started := make(chan struct{})
	f.srv.hook = blockAfterHalf(body, started)

	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	j, err := f.m.Install(ctx, "b1", BackendCPU)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := f.m.Install(ctx, "b1", BackendCPU); !errors.Is(err, ErrJobActive) {
		t.Errorf("second install = %v", err)
	}
	waitFor(t, func() bool { return f.m.Jobs()[0].Done == int64(len(body)/2) })
	if got := f.m.Jobs()[0]; got.State != StateDownloading || got.Step == "" {
		t.Errorf("job = %+v", got)
	}

	if err := f.m.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	j, err = f.m.Wait(ctx, j.ID)
	if err != nil || j.State != StateCanceled || j.Error != "" {
		t.Fatalf("job = %+v, %v", j, err)
	}
	if exists(filepath.Join(f.dir, j.ID)) {
		t.Error("canceled build looks installed")
	}
	info, err := os.Stat(filepath.Join(f.dir, tmpDirName, a.Name+".part"))
	if err != nil || info.Size() != int64(len(body)/2) {
		t.Fatalf("partial = %v, %v", info, err)
	}
	if err := f.m.Cancel(j.ID); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("cancel finished job = %v", err)
	}

	f.srv.mu.Lock()
	f.srv.hook = nil
	f.srv.ranges = nil
	f.srv.mu.Unlock()
	if j := f.install(t, "b1", BackendCPU); j.State != StateDone {
		t.Fatalf("resumed job = %+v", j)
	}
	if want := fmt.Sprintf("bytes=%d-", len(body)/2); !slices.Contains(f.srv.ranges, want) {
		t.Errorf("ranges = %q", f.srv.ranges)
	}
}

func TestJobsQueueAndCancelQueued(t *testing.T) {
	f := newFixture(t)
	a := f.release(t, "b1", routerScript)
	f.release(t, "b2", routerScript)
	started := make(chan struct{})
	blocker := blockAfterHalf(f.srv.files[a.Name], started)
	f.srv.hook = func(w http.ResponseWriter, r *http.Request) bool {
		return strings.Contains(r.URL.Path, "b1") && blocker(w, r)
	}
	ctx := context.Background()
	first, err := f.m.Install(ctx, "b1", BackendCPU)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := f.m.Install(ctx, "b2", BackendCPU)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != StateQueued {
		t.Errorf("second = %+v", second)
	}
	if err := f.m.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	if j, _ := f.m.Wait(ctx, second.ID); j.State != StateCanceled {
		t.Errorf("second = %+v", j)
	}
	if got := f.m.Jobs(); len(got) != 2 {
		t.Errorf("jobs = %+v", got)
	}
	f.m.Close()
	if j, _ := f.m.Wait(ctx, first.ID); j.State != StateCanceled {
		t.Errorf("first = %+v", j)
	}
	if _, err := f.m.Install(ctx, "b2", BackendCPU); !errors.Is(err, ErrClosed) {
		t.Errorf("after close = %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMaliciousArchiveFailsJob(t *testing.T) {
	f := newFixture(t)
	f.srv.add("x", nil)
	a := f.srv.add("llama-b1-bin-ubuntu-x64.tar.gz", tgz(t,
		entry{name: "llama-b1/llama-server", body: routerScript, mode: 0o755},
		entry{name: "llama-b1/evil", typeflag: tar.TypeSymlink, link: "../../../etc/passwd"},
	))
	f.rels["b1"] = releases.Release{Tag: "b1", BinaryTag: "b1", Assets: []releases.Asset{a}}
	j := f.install(t, "b1", BackendCPU)
	if j.State != StateFailed || !strings.Contains(j.Error, "unsafe archive") {
		t.Fatalf("job = %+v", j)
	}
	if exists(filepath.Join(f.dir, "b1-cpu")) || len(f.tmpEntries(t)) != 0 {
		t.Errorf("left behind: %v", f.tmpEntries(t))
	}
}

func TestProbeFailures(t *testing.T) {
	t.Run("not a program", func(t *testing.T) {
		f := newFixture(t)
		a := f.srv.add("llama-b1-bin-ubuntu-x64.tar.gz", tgz(t, entry{name: "llama-b1/other", body: "x"}))
		f.rels["b1"] = releases.Release{Tag: "b1", BinaryTag: "b1", Assets: []releases.Asset{a}}
		if j := f.install(t, "b1", BackendCPU); j.State != StateFailed || !strings.Contains(j.Error, "--version") {
			t.Fatalf("job = %+v", j)
		}
		if exists(filepath.Join(f.dir, "b1-cpu")) {
			t.Error("failed build installed")
		}
	})
	t.Run("exits non-zero", func(t *testing.T) {
		f := newFixture(t)
		f.release(t, "b1", "#!/bin/sh\necho 'missing libfoo.so'\nexit 127\n")
		if j := f.install(t, "b1", BackendCPU); j.State != StateFailed || !strings.Contains(j.Error, "missing libfoo.so") {
			t.Fatalf("job = %+v", j)
		}
	})
	t.Run("hangs", func(t *testing.T) {
		f := newFixture(t)
		f.release(t, "b1", "#!/bin/sh\nexec sleep 60\n")
		f.m.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			return runCommand(ctx, name, args...)
		}
		if j := f.install(t, "b1", BackendCPU); j.State != StateFailed {
			t.Fatalf("job = %+v", j)
		}
	})
}

func TestActivate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.mustInstall(t, "b1")
	f.release(t, "b2", plainScript)
	if j := f.install(t, "b2", BackendCPU); j.State != StateDone {
		t.Fatalf("job = %+v", j)
	}
	man, _ := f.m.Get("b2-cpu")
	if man.Probe.Router || man.Probe.Version != "100" || man.Probe.Commit != "abc1234" {
		t.Errorf("probe = %+v", man.Probe)
	}

	if err := f.m.Activate(ctx, "b2-cpu"); !errors.Is(err, ErrUnsupportedBuild) {
		t.Errorf("activate b2 = %v", err)
	}
	if err := f.m.Activate(ctx, "b3-cpu"); !errors.Is(err, ErrNotFound) {
		t.Errorf("activate b3 = %v", err)
	}
	if err := f.m.Activate(ctx, "../b1-cpu"); !errors.Is(err, ErrInvalidID) {
		t.Errorf("activate traversal = %v", err)
	}
	if err := f.m.Activate(ctx, "b1-cpu"); err != nil {
		t.Fatal(err)
	}
	if id, err := f.m.Active(ctx); err != nil || id != "b1-cpu" {
		t.Errorf("active = %q, %v", id, err)
	}

	// A lost volume means no active build rather than a broken one.
	if err := os.RemoveAll(filepath.Join(f.dir, "b1-cpu")); err != nil {
		t.Fatal(err)
	}
	if id, err := f.m.Active(ctx); err != nil || id != "" {
		t.Errorf("active = %q, %v", id, err)
	}
}

func TestListIgnoresNonBuilds(t *testing.T) {
	f := newFixture(t)
	if list, err := f.m.List(); err != nil || len(list) != 0 {
		t.Fatalf("missing dir: %v, %v", list, err)
	}
	f.mustInstall(t, "b1")
	for _, name := range []string{"b9-cpu", "lost+found", "bad"} {
		if err := os.MkdirAll(filepath.Join(f.dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.dir, "bad", manifestName), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.dir, "b8-cpu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "b8-cpu", manifestName), []byte(`{"id":"b1-cpu"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := f.m.List()
	if err != nil || len(list) != 1 || list[0].ID != "b1-cpu" {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestRemove(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.mustInstall(t, "b1")
	f.mustInstall(t, "b2")
	if err := f.m.Activate(ctx, "b1-cpu"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Remove(ctx, "b1-cpu"); !errors.Is(err, ErrActiveBuild) {
		t.Errorf("remove active = %v", err)
	}
	outside := filepath.Join(filepath.Dir(f.dir), "keep")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../keep", "b1-cpu/..", "..", ".", "", "b1/cpu", "/etc", "b1-cpu/", ".tmp", "x-cpu", "b1-nope"} {
		if err := f.m.Remove(ctx, id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("remove %q = %v", id, err)
		}
	}
	if !exists(outside) {
		t.Error("removed outside the build directory")
	}
	if err := f.m.Remove(ctx, "b7-cpu"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove missing = %v", err)
	}
	if err := f.m.Remove(ctx, "b2-cpu"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.dir, "b2-cpu")) || len(f.tmpEntries(t)) != 0 {
		t.Errorf("left behind: %v", f.tmpEntries(t))
	}
}

func TestApplyRetention(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := 0
	var mu sync.Mutex
	f.m.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		tick++
		return base.Add(time.Duration(tick) * time.Hour)
	}
	for _, tag := range []string{"b1", "b2", "b3", "b4"} {
		f.mustInstall(t, tag)
	}
	if err := f.m.Activate(ctx, "b1-cpu"); err != nil {
		t.Fatal(err)
	}

	// A partial far older than the clock's "now" is abandoned; a recent one is not.
	old := filepath.Join(f.dir, tmpDirName, "old.part")
	recent := filepath.Join(f.dir, tmpDirName, "recent.part")
	for _, p := range []string{old, recent} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	f.m.now = func() time.Time { return now }
	if err := os.Chtimes(old, now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	removed, err := f.m.ApplyRetention(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"b2-cpu"}) {
		t.Errorf("removed = %v", removed)
	}
	list, _ := f.m.List()
	var ids []string
	for _, man := range list {
		ids = append(ids, man.ID)
	}
	if !slices.Equal(ids, []string{"b4-cpu", "b3-cpu", "b1-cpu"}) {
		t.Errorf("kept = %v", ids)
	}
	if exists(old) || !exists(recent) {
		t.Errorf("old partial %v, recent partial %v", exists(old), exists(recent))
	}

	removed, err = f.m.ApplyRetention(ctx, 0)
	if err != nil || !slices.Equal(removed, []string{"b4-cpu", "b3-cpu"}) {
		t.Errorf("removed = %v, %v", removed, err)
	}
}

func TestEnsureFirstInstall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("none", func(t *testing.T) {
		f := newFixture(t)
		if err := f.m.EnsureFirstInstall(ctx, NoInstall, AutoBackend); err != nil {
			t.Fatal(err)
		}
		if len(f.m.Jobs()) != 0 {
			t.Error("started a job")
		}
	})
	t.Run("auto picks from hardware and activates", func(t *testing.T) {
		f := newFixture(t)
		f.snap.RenderNodes = []hardware.RenderNode{{Vendor: hardware.VendorAMD}}
		a := f.srv.add("llama-b1-bin-ubuntu-vulkan-x64.tar.gz", serverArchive(t, "b1", routerScript))
		f.rels["stable"] = releases.Release{Tag: "v1.0", BinaryTag: "b1", Assets: []releases.Asset{a}}
		if err := f.m.EnsureFirstInstall(ctx, "stable", AutoBackend); err != nil {
			t.Fatal(err)
		}
		if id, _ := f.m.Active(ctx); id != "b1-vulkan" {
			t.Errorf("active = %q", id)
		}
	})
	t.Run("already active does no network", func(t *testing.T) {
		f := newFixture(t)
		f.mustInstall(t, "b1")
		if err := f.m.Activate(ctx, "b1-cpu"); err != nil {
			t.Fatal(err)
		}
		if err := f.m.EnsureFirstInstall(ctx, "b404", BackendCPU); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("installed but not active gets activated", func(t *testing.T) {
		f := newFixture(t)
		f.mustInstall(t, "b1")
		if err := f.m.EnsureFirstInstall(ctx, "b1", BackendCPU); err != nil {
			t.Fatal(err)
		}
		if id, _ := f.m.Active(ctx); id != "b1-cpu" {
			t.Errorf("active = %q", id)
		}
	})
	t.Run("failure is reported", func(t *testing.T) {
		f := newFixture(t)
		f.release(t, "b1", "#!/bin/sh\nexit 1\n")
		err := f.m.EnsureFirstInstall(ctx, "b1", BackendCPU)
		if err == nil || !strings.Contains(err.Error(), "failed") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("build without router support", func(t *testing.T) {
		f := newFixture(t)
		f.release(t, "b1", plainScript)
		if err := f.m.EnsureFirstInstall(ctx, "b1", BackendCPU); !errors.Is(err, ErrUnsupportedBuild) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestValidateID(t *testing.T) {
	for _, id := range []string{"b12110-cpu", "b1-sycl-fp16", "v0.9.0-vulkan", "v1.2-rc1-cuda"} {
		if err := ValidateID(id); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
	for _, id := range []string{"", "cpu", "-cpu", "b1-", "b1", "b1-gpu", "../b1-cpu", "b1-cpu/x", "b1/../b2-cpu", "master-1-cpu", "..-cpu"} {
		if err := ValidateID(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("%q: %v", id, err)
		}
	}
}

func TestListMeasuresSize(t *testing.T) {
	f := newFixture(t)
	f.mustInstall(t, "b1")
	if err := os.WriteFile(filepath.Join(f.dir, "b1-cpu", "extra"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := f.m.List()
	if err != nil || len(list) != 1 || list[0].SizeBytes < 1000 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	onDisk, err := os.ReadFile(filepath.Join(f.dir, "b1-cpu", manifestName))
	if err != nil || strings.Contains(string(onDisk), "size_bytes") {
		t.Errorf("size is measured, not stored: %s, %v", onDisk, err)
	}
}
