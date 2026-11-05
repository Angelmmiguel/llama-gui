package profiles

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
)

// Store is the part of internal/store that profiles need.
type Store interface {
	CreateProfile(ctx context.Context, p store.Profile) error
	GetProfile(ctx context.Context, id string) (store.Profile, error)
	ListProfiles(ctx context.Context) ([]store.Profile, error)
	UpdateProfile(ctx context.Context, p store.Profile) error
	DeleteProfile(ctx context.Context, id string) error
}

// Service is CRUD over profiles that keeps the preset file in step. Lookup errors
// are store.ErrNotFound; a duplicate alias is store.ErrConflict.
type Service struct {
	store      Store
	modelsDir  string
	presetPath string

	mu        sync.Mutex // serializes changes, so the alias check and the write are one step
	supported func(string) bool
	skipped   []Skipped
	byAlias   map[string]Profile
}

// NewService returns a service. Call Sync once before relying on the preset or on
// Allow and IdlePolicy.
func NewService(st Store, modelsDir, presetPath string) *Service {
	return &Service{store: st, modelsDir: modelsDir, presetPath: presetPath, byAlias: map[string]Profile{}}
}

// SetSupported sets the active build's flag filter; call Sync afterwards.
func (s *Service) SetSupported(f func(flag string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.supported = f
}

// Sync renders the preset from the stored profiles and writes it.
func (s *Service) Sync(ctx context.Context) ([]Skipped, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(ctx); err != nil {
		return nil, err
	}
	return s.skipped, nil
}

func (s *Service) syncLocked(ctx context.Context) error {
	ps, err := s.List(ctx)
	if err != nil {
		return err
	}
	content, skipped := RenderPreset(ps, s.modelsDir, s.supported)
	if err := WritePreset(s.presetPath, content); err != nil {
		return err
	}
	s.skipped = skipped
	s.byAlias = make(map[string]Profile, len(ps))
	for _, p := range ps {
		s.byAlias[p.Alias] = p
	}
	return nil
}

// Skipped returns what the last Sync left out of the preset.
func (s *Service) Skipped() []Skipped {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipped
}

// Allow reports whether a request may load the model on demand. It implements
// the proxy's policy.
func (s *Service) Allow(alias string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byAlias[alias].OnDemand
}

// IdlePolicy returns how long the model may stay idle, if it has a limit.
func (s *Service) IdlePolicy(alias string) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.byAlias[alias].IdleUnloadSeconds
	return time.Duration(n) * time.Second, n > 0
}

// Command is the package-level Command with this service's models dir.
func (s *Service) Command(p Profile) string { return Command(p, s.modelsDir) }

func decode(row store.Profile) (Profile, error) {
	var p Profile
	if err := json.Unmarshal(row.Doc, &p); err != nil {
		return Profile{}, fmt.Errorf("profiles: decode %s: %w", row.ID, err)
	}
	p.ID = row.ID
	return p, nil
}

func encode(p Profile) (store.Profile, error) {
	doc, err := json.Marshal(p)
	if err != nil {
		return store.Profile{}, fmt.Errorf("profiles: encode: %w", err)
	}
	return store.Profile{ID: p.ID, Doc: doc}, nil
}

// List returns every profile.
func (s *Service) List(ctx context.Context) ([]Profile, error) {
	rows, err := s.store.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		p, err := decode(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Get returns one profile.
func (s *Service) Get(ctx context.Context, id string) (Profile, error) {
	row, err := s.store.GetProfile(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	return decode(row)
}

func (s *Service) checkAliasFree(ctx context.Context, p Profile) error {
	all, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, o := range all {
		if o.Alias == p.Alias && o.ID != p.ID {
			return fmt.Errorf("%w: alias %q is taken", store.ErrConflict, p.Alias)
		}
	}
	return nil
}

// Create stores a new profile, assigning its id when empty, and re-renders the preset.
func (s *Service) Create(ctx context.Context, p Profile) (Profile, error) {
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkAliasFree(ctx, p); err != nil {
		return Profile{}, err
	}
	if p.ID == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return Profile{}, fmt.Errorf("profiles: id: %w", err)
		}
		p.ID = hex.EncodeToString(b)
	}
	row, err := encode(p)
	if err != nil {
		return Profile{}, err
	}
	if err := s.store.CreateProfile(ctx, row); err != nil {
		return Profile{}, err
	}
	return p, s.syncLocked(ctx)
}

// Update replaces the profile with this id and re-renders the preset.
func (s *Service) Update(ctx context.Context, id string, p Profile) (Profile, error) {
	p.ID = id
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkAliasFree(ctx, p); err != nil {
		return Profile{}, err
	}
	row, err := encode(p)
	if err != nil {
		return Profile{}, err
	}
	if err := s.store.UpdateProfile(ctx, row); err != nil {
		return Profile{}, err
	}
	return p, s.syncLocked(ctx)
}

// Delete removes a profile and re-renders the preset.
func (s *Service) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteProfile(ctx, id); err != nil {
		return err
	}
	return s.syncLocked(ctx)
}
