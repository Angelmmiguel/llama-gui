package proxy

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Phases of an in-flight request.
const (
	PhaseWaiting    = "waiting"    // nothing from upstream yet: queued or in prefill
	PhaseResponding = "responding" // the body is flowing
)

// Inflight is a request in progress.
type Inflight struct {
	ID        uint64    `json:"id"`
	Model     string    `json:"model"`
	Endpoint  string    `json:"endpoint"`
	StartedAt time.Time `json:"started_at"`
	Slot      *int      `json:"slot,omitempty"`
	Task      *int64    `json:"task,omitempty"`
	Phase     string    `json:"phase"`
}

type entry struct {
	Inflight
	cancel context.CancelFunc
}

type assignment struct {
	slot *int
	task *int64
}

type registry struct {
	ids     atomic.Uint64
	mu      sync.Mutex
	entries []*entry // oldest first
}

func (r *registry) nextID() uint64 { return r.ids.Add(1) }

func (r *registry) add(model, endpoint string, started time.Time, cancel context.CancelFunc) *entry {
	e := &entry{Inflight: Inflight{ID: r.nextID(), Model: model, Endpoint: endpoint, StartedAt: started, Phase: PhaseWaiting}, cancel: cancel}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return e
}

func (r *registry) remove(e *entry) (*int, *int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = slices.DeleteFunc(r.entries, func(o *entry) bool { return o == e })
	return e.Slot, e.Task
}

func (r *registry) responding(e *entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Phase = PhaseResponding
}

func (r *registry) count(model string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.entries {
		if e.Model == model {
			n++
		}
	}
	return n
}

func (r *registry) list() []Inflight {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Inflight, 0, len(r.entries))
	for _, e := range r.entries {
		c := e.Inflight
		if e.Slot != nil {
			s := *e.Slot
			c.Slot = &s
		}
		if e.Task != nil {
			t := *e.Task
			c.Task = &t
		}
		out = append(out, c)
	}
	return out
}

func (r *registry) assign(model string, slot int, task int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var target *entry
	for _, e := range r.entries {
		if e.Model != model {
			continue
		}
		if e.Task != nil && *e.Task == task && e.Slot != nil && *e.Slot == slot {
			return false
		}
		if target == nil && e.Task == nil {
			target = e
		}
	}
	if target == nil {
		return false
	}
	// A newer task on the slot means whoever held it is done upstream, and
	// Cancel must not reach that request instead.
	for _, e := range r.entries {
		if e.Model == model && e.Slot != nil && *e.Slot == slot {
			e.Slot = nil
		}
	}
	target.Slot, target.Task = &slot, &task
	return true
}

func (r *registry) cancel(model string, slot int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.Model == model && e.Slot != nil && *e.Slot == slot {
			e.cancel()
			return true
		}
	}
	return false
}
