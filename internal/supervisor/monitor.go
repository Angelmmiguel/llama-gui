package supervisor

import (
	"context"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/llama"
)

const requestTimeout = 10 * time.Second

// monitor restores wanted, then polls /models to keep the remembered set current
// and unload idle models. remembered stays untouched until the restore is done, so
// a crash in between does not forget what should be loaded.
func (s *Supervisor) monitor(ctx context.Context, client *llama.Client, wanted []string) {
	tick := time.NewTicker(s.opts.PollInterval)
	defer tick.Stop()
	restored := len(wanted) == 0
	for {
		if !restored {
			restored = s.restore(ctx, client, wanted)
		}
		if restored {
			s.poll(ctx, client)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Supervisor) restore(ctx context.Context, client *llama.Client, wanted []string) bool {
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	models, err := client.Models(rctx)
	if err != nil {
		return false
	}
	// A profile may have gone since, and load-on-startup may have got there first.
	unloaded := make(map[string]bool, len(models))
	for _, m := range models {
		if m.Status == "unloaded" {
			unloaded[m.ID] = true
		}
	}
	for _, id := range wanted {
		if !unloaded[id] {
			continue
		}
		if err := client.Load(rctx, id); err != nil {
			s.log.Warn("reload after restart failed", "model", id, "error", err)
		} else {
			s.log.Info("reloading model after restart", "model", id)
		}
	}
	return true
}

func (s *Supervisor) remember(models []llama.Model) {
	var active []string
	for _, m := range models {
		switch m.Status {
		case "loaded", "loading", "sleeping":
			active = append(active, m.ID)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remembered = active
}

func (s *Supervisor) poll(ctx context.Context, client *llama.Client) {
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	models, err := client.Models(rctx)
	if err != nil {
		return
	}
	s.remember(models)
	now := s.now()
	var candidates []candidate
	for _, m := range models {
		if m.Status != "loaded" {
			continue
		}
		c := candidate{model: m.ID}
		if s.opts.IdlePolicy != nil {
			if limit, ok := s.opts.IdlePolicy(m.ID); ok {
				c.limit = limit
			}
		}
		if s.opts.InFlight != nil {
			c.inFlight = s.opts.InFlight(m.ID)
		}
		candidates = append(candidates, c)
	}
	s.idleMu.Lock()
	due := s.idle.due(now, candidates)
	s.idleMu.Unlock()
	for _, id := range due {
		s.log.Info("unloading idle model", "model", id)
		if err := client.Unload(rctx, id); err != nil {
			s.log.Warn("idle unload failed", "model", id, "error", err)
		}
	}
}

// candidate is one loaded model at a poll. A limit of 0 means no idle unload.
type candidate struct {
	model    string
	limit    time.Duration
	inFlight int
}

// idleTracker decides which models have been idle too long. Not safe for concurrent use.
type idleTracker struct {
	last   map[string]time.Time
	loaded map[string]bool
}

func (t *idleTracker) touch(model string, now time.Time) {
	if t.last == nil {
		t.last = map[string]time.Time{}
	}
	t.last[model] = now
}

// due returns the models to unload now. A model's timer restarts when it becomes
// loaded, however it got there, so one reloaded long after its last request is
// not unloaded on sight.
func (t *idleTracker) due(now time.Time, loaded []candidate) []string {
	var out []string
	seen := make(map[string]bool, len(loaded))
	for _, c := range loaded {
		seen[c.model] = true
		if !t.loaded[c.model] {
			t.touch(c.model, now)
		}
		if c.limit <= 0 {
			continue
		}
		if c.inFlight > 0 {
			t.touch(c.model, now)
			continue
		}
		if now.Sub(t.last[c.model]) >= c.limit {
			out = append(out, c.model)
			// If the unload fails or is slow, wait a full period before asking again.
			t.touch(c.model, now)
		}
	}
	t.loaded = seen
	return out
}
