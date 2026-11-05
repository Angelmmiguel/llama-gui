package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
)

// Body is what capture kept of one request, truncated to MaxBodyCapture.
type Body struct {
	RequestID uint64
	Request   []byte
	Response  []byte
}

// Sink receives measurements. Both methods run on the request path and must not
// block. A RecordBody comes right before the Record with the same ID.
type Sink interface {
	Record(RequestRecord)
	RecordBody(Body)
}

// RequestStore is the part of internal/store that StoreSink needs.
type RequestStore interface {
	InsertRequest(ctx context.Context, r store.Request) (int64, error)
	InsertRequestBody(ctx context.Context, requestID int64, request, response []byte) error
}

const (
	writeTimeout       = 10 * time.Second
	maxQueuedBodyBytes = 64 << 20
	warnEveryDrops     = 100
)

// StoreSink writes to the store from its own goroutine. When the queue is full,
// because the disk has stalled, it drops rather than hold up traffic.
type StoreSink struct {
	st    RequestStore
	log   *slog.Logger
	queue chan item
	quit  chan struct{}
	done  chan struct{}

	mu          sync.Mutex
	waiting     map[uint64]Body // between a RecordBody and its Record
	queuedBytes atomic.Int64
	dropped     atomic.Int64
}

type item struct {
	rec     RequestRecord
	body    Body
	hasBody bool
}

// NewStoreSink starts a sink with room for queue records; 0 means 1024. Close stops it.
func NewStoreSink(st RequestStore, log *slog.Logger, queue int) *StoreSink {
	if log == nil {
		log = slog.Default()
	}
	if queue <= 0 {
		queue = 1024
	}
	s := &StoreSink{st: st, log: log, queue: make(chan item, queue), quit: make(chan struct{}), done: make(chan struct{}), waiting: map[uint64]Body{}}
	go s.loop()
	return s
}

func bodySize(b Body) int64 { return int64(len(b.Request) + len(b.Response)) }

// RecordBody holds the body for its Record, dropping it past a total memory bound.
func (s *StoreSink) RecordBody(b Body) {
	if s.queuedBytes.Add(bodySize(b)) > maxQueuedBodyBytes {
		s.queuedBytes.Add(-bodySize(b))
		s.warnDrop("body")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiting[b.RequestID] = b
}

// Record queues the record and its body, if any.
func (s *StoreSink) Record(r RequestRecord) {
	s.mu.Lock()
	body, hasBody := s.waiting[r.ID]
	delete(s.waiting, r.ID)
	s.mu.Unlock()
	select {
	case s.queue <- item{r, body, hasBody}:
	default:
		s.queuedBytes.Add(-bodySize(body))
		s.warnDrop("record")
	}
}

func (s *StoreSink) warnDrop(what string) {
	if n := s.dropped.Add(1); n%warnEveryDrops == 1 {
		s.log.Warn("dropping request measurements, the store is not keeping up", "what", what, "dropped_total", n)
	}
}

func (s *StoreSink) loop() {
	defer close(s.done)
	for {
		select {
		case it := <-s.queue:
			s.write(it)
		case <-s.quit:
			for {
				select {
				case it := <-s.queue:
					s.write(it)
				default:
					return
				}
			}
		}
	}
}

func (s *StoreSink) write(it item) {
	defer s.queuedBytes.Add(-bodySize(it.body))
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	id, err := s.st.InsertRequest(ctx, toStore(it.rec))
	if err != nil {
		s.log.Warn("store request failed", "error", err)
		return
	}
	if it.hasBody {
		if err := s.st.InsertRequestBody(ctx, id, it.body.Request, it.body.Response); err != nil {
			s.log.Warn("store request body failed", "error", err)
		}
	}
}

// Close writes what is queued and stops. Records arriving afterwards are lost.
func (s *StoreSink) Close(ctx context.Context) error {
	close(s.quit)
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func toStore(r RequestRecord) store.Request {
	out := store.Request{
		StartedAt: r.StartedAt, Model: r.Model, Endpoint: r.Endpoint, Method: r.Method, Status: r.Status,
		Slot: r.Slot, TaskID: r.Task, TTFTMs: r.TTFTMs, TotalMs: r.TotalMs,
		PromptN: r.Timings.PromptN, CacheN: r.Timings.CacheN, PromptMs: r.Timings.PromptMS,
		PredictedN: r.Timings.PredictedN, PredictedMs: r.Timings.PredictedMS,
		PredictedPerSecond: r.Timings.PredictedPerSecond, Stream: r.Stream,
	}
	// A bare 2xx says nothing the status doesn't.
	if r.StatusClass != strconv.Itoa(r.Status) || r.Status >= 400 {
		out.Error = r.StatusClass
	}
	if len(r.Gaps) > 0 {
		if b, err := json.Marshal(r.Gaps); err == nil {
			out.Gaps = b
		}
	}
	return out
}
