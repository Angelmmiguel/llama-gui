package library

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"sync"
	"time"
)

const (
	// catalogTTL is how long the catalog's answers stay fresh. It is a short list of well-known
	// repos, whose files rarely change, and an old answer is still a useful one.
	catalogTTL     = 24 * time.Hour
	catalogRefresh = 2 * time.Minute
)

// CatalogHub answers for the starter catalog's repos from what it has kept, so opening the Library
// costs nothing, even just after a restart. Stale answers are served while a refresh runs behind
// them. Everything else goes straight to the Client.
type CatalogHub struct {
	*Client
	entries []CatalogEntry
	known   map[string]bool
	save    func(ctx context.Context, data []byte) error
	log     *slog.Logger
	now     func() time.Time
	// ctx ends the background refresh along with the service.
	ctx context.Context

	mu         sync.Mutex
	snapshot   catalogSnapshot
	refreshing bool
	done       chan struct{}
}

type catalogSnapshot struct {
	At    time.Time              `json:"at"`
	Repos map[string]catalogRepo `json:"repos"`
}

type catalogRepo struct {
	Info RepoInfo            `json:"info"`
	Meta map[string]Metadata `json:"meta"`
}

// NewCatalogHub starts from saved, the bytes an earlier run passed to save, if they are readable.
func NewCatalogHub(ctx context.Context, client *Client, saved []byte, save func(ctx context.Context, data []byte) error, log *slog.Logger) *CatalogHub {
	h := &CatalogHub{
		Client: client, entries: Catalog(), known: map[string]bool{}, save: save, log: log, now: time.Now, ctx: ctx,
		snapshot: catalogSnapshot{Repos: map[string]catalogRepo{}},
	}
	for _, e := range h.entries {
		h.known[e.Repo] = true
	}
	var old catalogSnapshot
	if len(saved) > 0 && json.Unmarshal(saved, &old) == nil && old.Repos != nil {
		h.snapshot = old
	}
	return h
}

// Repo answers from the snapshot for a catalog repo.
func (h *CatalogHub) Repo(ctx context.Context, repo string) (RepoInfo, error) {
	if !h.known[repo] {
		return h.Client.Repo(ctx, repo)
	}
	h.mu.Lock()
	r, ok := h.snapshot.Repos[repo]
	h.mu.Unlock()
	if ok {
		h.refreshIfStale()
		return r.Info, nil
	}
	info, err := h.Client.Repo(ctx, repo)
	if err != nil {
		return RepoInfo{}, err
	}
	h.remember(ctx, repo, func(r *catalogRepo) { r.Info = info })
	return info, nil
}

// ReadRemoteMetadata answers from the snapshot for a file of a catalog repo.
func (h *CatalogHub) ReadRemoteMetadata(ctx context.Context, repo, file string) (Metadata, error) {
	if !h.known[repo] {
		return h.Client.ReadRemoteMetadata(ctx, repo, file)
	}
	h.mu.Lock()
	m, ok := h.snapshot.Repos[repo].Meta[file]
	h.mu.Unlock()
	if ok {
		return m, nil
	}
	m, err := h.Client.ReadRemoteMetadata(ctx, repo, file)
	if err != nil {
		return Metadata{}, err
	}
	h.remember(ctx, repo, func(r *catalogRepo) {
		if r.Meta == nil {
			r.Meta = map[string]Metadata{}
		}
		r.Meta[file] = m
	})
	return m, nil
}

// Candidates is the catalog resolved from what is kept, asking the hub only for what is missing.
func (h *CatalogHub) Candidates(ctx context.Context) ([]Candidate, error) {
	cands := ResolveCatalog(ctx, h, h.entries)
	if len(cands) == 0 && len(h.entries) > 0 {
		// Nothing resolved: recover the reason, so the Library says the hub is out of reach.
		if _, err := h.Repo(ctx, h.entries[0].Repo); err != nil {
			return nil, err
		}
	}
	return cands, nil
}

func (h *CatalogHub) remember(ctx context.Context, repo string, change func(*catalogRepo)) {
	h.mu.Lock()
	r := h.snapshot.Repos[repo]
	change(&r)
	h.snapshot.Repos[repo] = r
	if h.snapshot.At.IsZero() {
		h.snapshot.At = h.now()
	}
	data, err := json.Marshal(h.snapshot)
	h.mu.Unlock()
	if err == nil {
		err = h.save(ctx, data)
	}
	if err != nil {
		h.log.Warn("saving the Library catalog", "error", err)
	}
}

func (h *CatalogHub) refreshIfStale() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refreshing || h.now().Sub(h.snapshot.At) < catalogTTL {
		return
	}
	h.refreshing = true
	h.done = make(chan struct{})
	go h.refresh(h.done)
}

// refresh replaces only what the hub answered, so being offline keeps the old answers.
func (h *CatalogHub) refresh(done chan struct{}) {
	defer close(done)
	ctx, cancel := context.WithTimeout(h.ctx, catalogRefresh)
	defer cancel()
	type fresh struct {
		repo string
		r    catalogRepo
	}
	results := make([]fresh, 0, len(h.entries))
	for _, e := range h.entries {
		info, err := h.Client.Repo(ctx, e.Repo)
		if err != nil {
			h.log.Debug("refreshing the Library catalog", "repo", e.Repo, "error", err)
			continue
		}
		h.mu.Lock()
		old := h.snapshot.Repos[e.Repo].Meta
		h.mu.Unlock()
		// A header is kept unless it can be read again, as losing it would mean asking on the next
		// page view. A file gone from the repo is simply never asked for.
		meta := maps.Clone(old)
		if meta == nil {
			meta = map[string]Metadata{}
		}
		for file := range old {
			if m, err := h.Client.ReadRemoteMetadata(ctx, e.Repo, file); err == nil {
				meta[file] = m
			}
		}
		results = append(results, fresh{e.Repo, catalogRepo{Info: info, Meta: meta}})
	}
	h.mu.Lock()
	h.refreshing = false
	for _, f := range results {
		h.snapshot.Repos[f.repo] = f.r
	}
	if len(results) > 0 {
		h.snapshot.At = h.now()
	}
	data, err := json.Marshal(h.snapshot)
	h.mu.Unlock()
	if len(results) == 0 {
		return
	}
	if err == nil {
		err = h.save(ctx, data)
	}
	if err != nil {
		h.log.Warn("saving the Library catalog", "error", err)
	}
}
