package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// catalogServer answers for any repo, counting what it is asked.
func catalogServer(t *testing.T, gguf []byte, down *atomic.Bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		switch {
		case len(r.URL.Path) > 11 && r.URL.Path[len(r.URL.Path)-10:] == "/tree/main":
			_, _ = fmt.Fprint(w, `[{"type":"file","path":"m-Q8_0.gguf","size":800,"lfs":{"oid":"abc","size":800}}]`)
		case len(r.URL.Path) > 11 && r.URL.Path[:11] == "/api/models":
			_, _ = fmt.Fprint(w, `{"downloads":1}`)
		default:
			_, _ = w.Write(gguf)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

type savedCatalog struct {
	mu   sync.Mutex
	data []byte
}

func (s *savedCatalog) save(_ context.Context, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = b
	return nil
}

func (s *savedCatalog) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

func testGGUF() []byte {
	b := &ggufBuilder{}
	b.kvStr("general.architecture", "llama")
	b.kvU32("llama.block_count", 12)
	return b.bytes(3, 1)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCatalogHubAnswersFromWhatItKept(t *testing.T) {
	var down atomic.Bool
	srv, hits := catalogServer(t, testGGUF(), &down)
	store := &savedCatalog{}
	ctx := t.Context()
	first := NewCatalogHub(ctx, NewClient(srv.Client(), srv.URL, ""), nil, store.save, quietLog())
	cands, err := first.Candidates(ctx)
	if err != nil || len(cands) != len(Catalog()) {
		t.Fatalf("%d candidates, %v", len(cands), err)
	}
	if hits.Load() == 0 {
		t.Fatal("nothing asked of the hub on a cold start")
	}

	// A restart: another hub, with what the first one saved, and a hub that is out of reach.
	down.Store(true)
	asked := hits.Load()
	second := NewCatalogHub(ctx, NewClient(srv.Client(), srv.URL, ""), store.bytes(), store.save, quietLog())
	again, err := second.Candidates(ctx)
	if err != nil || len(again) != len(Catalog()) {
		t.Fatalf("after a restart: %d candidates, %v", len(again), err)
	}
	if got := hits.Load(); got != asked {
		t.Errorf("asked the hub %d more times after a restart", got-asked)
	}
	if again[0].Meta.BlockCount != 12 || len(again[0].Variants) != 1 {
		t.Errorf("%+v", again[0])
	}
}

func TestCatalogHubRefreshesBehindStaleAnswers(t *testing.T) {
	var down atomic.Bool
	srv, hits := catalogServer(t, testGGUF(), &down)
	store := &savedCatalog{}
	ctx := t.Context()
	h := NewCatalogHub(ctx, NewClient(srv.Client(), srv.URL, ""), nil, store.save, quietLog())
	if _, err := h.Candidates(ctx); err != nil {
		t.Fatal(err)
	}
	repo := Catalog()[0].Repo

	now := time.Now()
	h.now = func() time.Time { return now }
	if _, err := h.Repo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if h.done != nil {
		t.Fatal("refreshed while fresh")
	}

	// Stale and offline: the old answer still comes back, and survives the failed refresh.
	h.Client.now = func() time.Time { return now.Add(48 * time.Hour) }
	h.now = func() time.Time { return now.Add(48 * time.Hour) }
	down.Store(true)
	asked := hits.Load()
	if _, err := h.Repo(ctx, repo); err != nil {
		t.Fatalf("stale answer: %v", err)
	}
	<-h.done
	if hits.Load() == asked {
		t.Error("no refresh was tried")
	}
	if info, err := h.Repo(ctx, repo); err != nil || len(info.Variants) != 1 {
		t.Errorf("lost the old answer: %+v %v", info, err)
	}
}

func TestCatalogHubLeavesOtherReposAlone(t *testing.T) {
	var down atomic.Bool
	srv, hits := catalogServer(t, testGGUF(), &down)
	h := NewCatalogHub(t.Context(), NewClient(srv.Client(), srv.URL, ""), nil, (&savedCatalog{}).save, quietLog())
	if _, err := h.Repo(t.Context(), "someone/else"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == 0 {
		t.Error("did not go to the hub")
	}
	if len(h.snapshot.Repos) != 0 {
		t.Errorf("kept a repo outside the catalog: %v", h.snapshot.Repos)
	}
}

func TestCatalogHubIgnoresWhatItCannotRead(t *testing.T) {
	var down atomic.Bool
	srv, _ := catalogServer(t, testGGUF(), &down)
	h := NewCatalogHub(t.Context(), NewClient(srv.Client(), srv.URL, ""), []byte("not json"), (&savedCatalog{}).save, quietLog())
	if cands, err := h.Candidates(t.Context()); err != nil || len(cands) == 0 {
		t.Errorf("%d %v", len(cands), err)
	}
	if _, err := json.Marshal(h.snapshot); err != nil {
		t.Error(err)
	}
}

func TestCatalogHubReportsAHubOutOfReach(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	srv, _ := catalogServer(t, testGGUF(), &down)
	h := NewCatalogHub(t.Context(), NewClient(srv.Client(), srv.URL, ""), nil, (&savedCatalog{}).save, quietLog())
	if _, err := h.Candidates(t.Context()); err == nil {
		t.Error("a cold start with no hub reported no error")
	}
}

func TestCatalogHubSaysWhyTheCatalogIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	h := NewCatalogHub(t.Context(), NewClient(srv.Client(), srv.URL, ""), nil, (&savedCatalog{}).save, quietLog())
	if _, err := h.Candidates(t.Context()); !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v", err)
	}
}
