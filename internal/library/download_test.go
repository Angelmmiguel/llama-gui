package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type dlHub struct {
	files     map[string][]byte
	badSHA    map[string]bool
	ignoreRng bool
	stall     chan struct{} // when set, responses send a little data and then wait
	mu        sync.Mutex
	ranges    []string
}

func (h *dlHub) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/u/r", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":"u/r"}`) })
	mux.HandleFunc("/api/models/u/r/tree/main", func(w http.ResponseWriter, r *http.Request) {
		var parts []string
		for name, data := range h.files {
			sum := sha256.Sum256(data)
			oid := hex.EncodeToString(sum[:])
			if h.badSHA[name] {
				oid = strings.Repeat("0", 64)
			}
			parts = append(parts, fmt.Sprintf(`{"type":"file","path":%q,"size":%d,"lfs":{"oid":%q,"size":%d}}`, name, len(data), oid, len(data)))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(parts, ","))
	})
	mux.HandleFunc("/u/r/resolve/main/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := h.files[strings.TrimPrefix(r.URL.Path, "/u/r/resolve/main/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		rng := r.Header.Get("Range")
		h.mu.Lock()
		h.ranges = append(h.ranges, rng)
		h.mu.Unlock()
		from := 0
		if rng != "" && !h.ignoreRng {
			fmt.Sscanf(rng, "bytes=%d-", &from)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(data)-1, len(data)))
			w.WriteHeader(http.StatusPartialContent)
		}
		if h.stall != nil {
			_, _ = w.Write(data[from : from+1000])
			from += 1000
			w.(http.Flusher).Flush()
			select {
			case <-h.stall:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(data[from:])
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func payload(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) + seed
	}
	return b
}

func newQueue(t *testing.T, h *dlHub, onFinish func()) (*Queue, string) {
	t.Helper()
	srv := h.server(t)
	dir := t.TempDir()
	q := NewQueue(dir, NewClient(srv.Client(), srv.URL, ""), nil, onFinish)
	q.free = func(string) (int64, error) { return 100 << 30, nil }
	t.Cleanup(q.Close)
	return q, dir
}

func waitFor(t *testing.T, q *Queue, id string, states ...JobState) Job {
	t.Helper()
	for range 1000 {
		for _, j := range q.List() {
			if j.ID == id {
				for _, s := range states {
					if j.State == s {
						return j
					}
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %v: %+v", id, states, q.List())
	return Job{}
}

func TestDownloadSingle(t *testing.T) {
	data := payload(900_000, 1)
	var finished atomic.Int32
	q, dir := newQueue(t, &dlHub{files: map[string][]byte{"m-Q4_K_M.gguf": data}}, func() { finished.Add(1) })
	j, err := q.Enqueue(context.Background(), "u/r", "m-Q4_K_M.gguf")
	if err != nil || j.Total != 900_000 || j.Variant != "Q4_K_M" {
		t.Fatalf("%+v %v", j, err)
	}
	if done := waitFor(t, q, j.ID, JobDone, JobFailed); done.State != JobDone || done.Done != done.Total {
		t.Fatalf("%+v", done)
	}
	got, err := os.ReadFile(filepath.Join(dir, "m-Q4_K_M.gguf"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("content mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "m-Q4_K_M.gguf.partial")); !os.IsNotExist(err) {
		t.Error("partial left behind")
	}
	for range 100 {
		if finished.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if finished.Load() != 1 {
		t.Errorf("onFinish called %d times", finished.Load())
	}
	if _, err := q.Enqueue(context.Background(), "u/r", "m-Q4_K_M.gguf"); !errors.Is(err, ErrExists) {
		t.Errorf("re-enqueue: %v", err)
	}
}

func TestDownloadResume(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		data := payload(900_000, 2)
		h := &dlHub{files: map[string][]byte{"m-Q8_0.gguf": data}, ignoreRng: ignore}
		q, dir := newQueue(t, h, nil)
		if err := os.WriteFile(filepath.Join(dir, "m-Q8_0.gguf.partial"), data[:400_000], 0o644); err != nil {
			t.Fatal(err)
		}
		j, err := q.Enqueue(context.Background(), "u/r", "m-Q8_0.gguf")
		if err != nil {
			t.Fatal(err)
		}
		if done := waitFor(t, q, j.ID, JobDone, JobFailed); done.State != JobDone {
			t.Fatalf("ignoreRange=%v: %+v", ignore, done)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "m-Q8_0.gguf")); !bytes.Equal(got, data) {
			t.Errorf("ignoreRange=%v: content mismatch", ignore)
		}
		if len(h.ranges) != 1 || h.ranges[0] != "bytes=400000-" {
			t.Errorf("ranges %v", h.ranges)
		}
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	h := &dlHub{files: map[string][]byte{"m-Q8_0.gguf": payload(50_000, 3)}, badSHA: map[string]bool{"m-Q8_0.gguf": true}}
	q, dir := newQueue(t, h, nil)
	j, _ := q.Enqueue(context.Background(), "u/r", "m-Q8_0.gguf")
	done := waitFor(t, q, j.ID, JobDone, JobFailed)
	if done.State != JobFailed || !strings.Contains(done.Error, "checksum") {
		t.Fatalf("%+v", done)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("files left: %v", entries)
	}
}

func TestDownloadCancel(t *testing.T) {
	for _, remove := range []bool{true, false} {
		h := &dlHub{files: map[string][]byte{"m-Q8_0.gguf": payload(900_000, 4)}, stall: make(chan struct{})}
		q, dir := newQueue(t, h, nil)
		j, _ := q.Enqueue(context.Background(), "u/r", "m-Q8_0.gguf")
		for range 400 {
			if l := q.List(); l[0].Done > 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err := q.Cancel(j.ID, remove); err != nil {
			t.Fatal(err)
		}
		waitFor(t, q, j.ID, JobCanceled)
		time.Sleep(50 * time.Millisecond) // partial removal happens right after the state change
		_, err := os.Stat(filepath.Join(dir, "m-Q8_0.gguf.partial"))
		if remove != os.IsNotExist(err) {
			t.Errorf("remove=%v: stat err %v", remove, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "m-Q8_0.gguf")); !os.IsNotExist(err) {
			t.Error("final file exists")
		}
	}
	q, _ := newQueue(t, &dlHub{files: map[string][]byte{}}, nil)
	if err := q.Cancel("nope", false); !errors.Is(err, ErrNoJob) {
		t.Errorf("%v", err)
	}
}

func TestDownloadQueuedCancelAndSerial(t *testing.T) {
	h := &dlHub{files: map[string][]byte{"a-Q8_0.gguf": payload(900_000, 5), "b-Q8_0.gguf": payload(1000, 6)}, stall: make(chan struct{})}
	q, dir := newQueue(t, h, nil)
	a, _ := q.Enqueue(context.Background(), "u/r", "a-Q8_0.gguf")
	b, _ := q.Enqueue(context.Background(), "u/r", "b-Q8_0.gguf")
	waitFor(t, q, a.ID, JobDownloading)
	if l := q.List(); l[1].State != JobQueued {
		t.Errorf("second job should wait: %+v", l[1])
	}
	if err := q.Cancel(b.ID, true); err != nil {
		t.Fatal(err)
	}
	close(h.stall)
	waitFor(t, q, a.ID, JobDone)
	if l := q.List(); l[1].State != JobCanceled {
		t.Errorf("%+v", l[1])
	}
	if _, err := os.Stat(filepath.Join(dir, "b-Q8_0.gguf")); !os.IsNotExist(err) {
		t.Error("canceled job downloaded")
	}
}

func TestDownloadDiskRefusal(t *testing.T) {
	h := &dlHub{files: map[string][]byte{"m-Q8_0.gguf": payload(10_000, 7)}}
	q, dir := newQueue(t, h, nil)
	q.free = func(string) (int64, error) { return diskReserve + 9_999, nil }
	if _, err := q.Enqueue(context.Background(), "u/r", "m-Q8_0.gguf"); !errors.Is(err, ErrNoSpace) {
		t.Errorf("got %v", err)
	}
	// A partial already on disk counts toward what is left to fetch.
	if err := os.WriteFile(filepath.Join(dir, "m-Q8_0.gguf.partial"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(context.Background(), "u/r", "m-Q8_0.gguf"); err != nil {
		t.Errorf("with partial: %v", err)
	}
}

func TestDownloadHostileNames(t *testing.T) {
	h := &dlHub{files: map[string][]byte{"m-Q8_0.gguf": payload(100, 8)}}
	q, dir := newQueue(t, h, nil)
	for _, name := range []string{
		"../x.gguf", "a/../x.gguf", "/etc/x.gguf", "..", "x.txt", "", "a\\b.gguf", ".hidden.gguf",
		"x.gguf\x00", "sub//x.gguf", "x..gguf", "a/./x.gguf", "x%2e%2e.gguf",
	} {
		if _, err := q.Enqueue(context.Background(), "u/r", name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q: %v", name, err)
		}
	}
	if _, err := q.Enqueue(context.Background(), "u/r", "other-Q8_0.gguf"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unlisted file: %v", err)
	}
	if _, err := q.Enqueue(context.Background(), "../r", "m-Q8_0.gguf"); !errors.Is(err, ErrInvalidRepo) {
		t.Errorf("repo: %v", err)
	}
	if _, err := q.Enqueue(context.Background(), "u/r"); err == nil {
		t.Error("no files accepted")
	}
	if entries, _ := os.ReadDir(filepath.Dir(dir)); len(entries) > 1 {
		t.Errorf("wrote outside the models dir: %v", entries)
	}
}

func TestDownloadDirectoryLayout(t *testing.T) {
	h := &dlHub{files: map[string][]byte{
		"g-Q4_K_M.gguf": payload(5000, 1), "mmproj-g-f16.gguf": payload(3000, 2),
		"S-Q6_K-00001-of-00002.gguf": payload(2000, 3), "S-Q6_K-00002-of-00002.gguf": payload(2000, 4),
	}}
	q, dir := newQueue(t, h, nil)
	a, err := q.Enqueue(context.Background(), "u/r", "g-Q4_K_M.gguf", "mmproj-g-f16.gguf")
	if err != nil {
		t.Fatal(err)
	}
	b, err := q.Enqueue(context.Background(), "u/r", "S-Q6_K-00001-of-00002.gguf", "S-Q6_K-00002-of-00002.gguf")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, q, a.ID, JobDone)
	waitFor(t, q, b.ID, JobDone)
	models, err := Scan(dir)
	if err != nil || len(models) != 2 {
		t.Fatalf("%+v %v", models, err)
	}
	var g, s LocalModel
	for _, m := range models {
		if m.ID == "g-Q4_K_M" {
			g = m
		} else {
			s = m
		}
	}
	if g.MMProj != "mmproj-g-f16.gguf" || g.SizeBytes != 8000 || s.ID != "S-Q6_K" || s.Shards != 2 {
		t.Errorf("g=%+v s=%+v", g, s)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("staging left behind: %v", entries)
	}
	if _, err := q.Enqueue(context.Background(), "u/r", "mmproj-g-f16.gguf"); !errors.Is(err, ErrNoModel) {
		t.Errorf("mmproj only: %v", err)
	}
}

func TestDownloadClose(t *testing.T) {
	h := &dlHub{files: map[string][]byte{"m-Q8_0.gguf": payload(900_000, 9)}, stall: make(chan struct{})}
	q, dir := newQueue(t, h, nil)
	j, _ := q.Enqueue(context.Background(), "u/r", "m-Q8_0.gguf")
	waitFor(t, q, j.ID, JobDownloading)
	done := make(chan struct{})
	go func() { q.Close(); q.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung")
	}
	if _, err := q.Enqueue(context.Background(), "u/r", "m-Q8_0.gguf"); !errors.Is(err, ErrClosed) {
		t.Errorf("after close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "m-Q8_0.gguf")); !os.IsNotExist(err) {
		t.Error("final file exists after close")
	}
}

func TestProgressSpeedAndETA(t *testing.T) {
	now := time.Unix(0, 0)
	q := &Queue{now: func() time.Time { return now }}
	j := &job{lastT: now}
	j.Total = 10_000
	q.progress(j, 0, 1000)
	if j.BytesPerSec != 0 {
		t.Error("speed before a full second")
	}
	now = now.Add(2 * time.Second)
	q.progress(j, 0, 3000)
	if j.BytesPerSec != 1500 || j.ETA != (10_000-3000)/1500 {
		t.Errorf("speed %d eta %d", j.BytesPerSec, j.ETA)
	}
}
