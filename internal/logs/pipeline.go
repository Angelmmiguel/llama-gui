package logs

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
)

const (
	defaultFlushEvery = 500 * time.Millisecond
	defaultFlushLines = 200
	defaultQueue      = 4096
	subscriberBuffer  = 512
	storeTimeout      = 5 * time.Second
	warnEveryDrops    = 1000
)

// PipelineStore is the part of internal/store the pipeline writes to and tails.
type PipelineStore interface {
	InsertLogLines(ctx context.Context, lines []store.LogLine) error
	InsertEvent(ctx context.Context, e store.Event) (int64, error)
	ListLogLines(ctx context.Context, f store.LogFilter) ([]store.LogLine, string, error)
}

// PipelineOptions configures a Pipeline. Store is required.
type PipelineOptions struct {
	Store PipelineStore
	// PortToModel is the fallback for lines from a child no spawn line announced.
	PortToModel func(int) string
	Clock       func() time.Time
	Logger      *slog.Logger
	FlushEvery  time.Duration
	FlushLines  int
	Queue       int
}

// Item is one entry of the live tail: a stored line or a stored event.
type Item struct {
	Line  *store.LogLine
	Event *store.Event
}

// Pipeline takes the router's output from OnLine to the store and the live
// tail. Nothing here may log line contents, which at a high -lv can hold prompts.
type Pipeline struct {
	opts   PipelineOptions
	log    *slog.Logger
	parser Parser
	det    Detector

	queue chan queued
	flush chan struct{} // nudges Run when a batch fills up

	portsMu sync.Mutex
	ports   map[int]string // learned from spawn lines, which precede a child's output

	dropMu  sync.Mutex
	dropped int64

	// mu covers a batch's insert and publish, so a Tail's backlog query and
	// subscription see each line exactly once.
	mu      sync.Mutex
	subs    map[int]chan Item
	nextSub int
}

type queued struct {
	line  *store.LogLine
	event *store.Event
}

// NewPipeline returns a Pipeline; Run drives it.
func NewPipeline(o PipelineOptions) *Pipeline {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = defaultFlushEvery
	}
	if o.FlushLines <= 0 {
		o.FlushLines = defaultFlushLines
	}
	if o.Queue <= 0 {
		o.Queue = defaultQueue
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{
		opts: o, log: log,
		queue: make(chan queued, o.Queue), flush: make(chan struct{}, 1),
		ports: map[int]string{}, subs: map[int]chan Item{},
	}
}

// OnLine parses one line of router output and queues it. It never blocks, since
// a stalled disk must not back up into the router's stdout.
func (p *Pipeline) OnLine(raw string, at time.Time) {
	line := p.parser.Parse(raw, at, p.modelForPort)
	stored := store.LogLine{TS: at, Level: line.Level, Source: line.Source, Model: line.Model, Message: line.Message, Raw: line.Raw}
	p.enqueue(queued{line: &stored})
	for _, e := range p.det.Observe(line) {
		if e.Kind == EventModelLoading && e.Meta["port"] != "" {
			p.learnPort(e.Meta["port"], e.Meta["model"])
		}
		// The supervisor reports this one itself, whatever the log says.
		if e.Kind == EventRouterStarted {
			continue
		}
		p.enqueueEvent(at, e.Kind, e.Severity, e.Title, e.Meta)
	}
}

func (p *Pipeline) learnPort(port, model string) {
	n, err := strconv.Atoi(port)
	if err != nil {
		return
	}
	p.portsMu.Lock()
	defer p.portsMu.Unlock()
	p.ports[n] = model
}

func (p *Pipeline) modelForPort(port int) string {
	p.portsMu.Lock()
	model, ok := p.ports[port]
	p.portsMu.Unlock()
	if !ok && p.opts.PortToModel != nil {
		return p.opts.PortToModel(port)
	}
	return model
}

// Event records something the log lines do not show, such as a build switch.
func (p *Pipeline) Event(kind, severity, title string, meta map[string]string) {
	p.enqueueEvent(p.opts.Clock(), kind, severity, title, meta)
}

func (p *Pipeline) enqueueEvent(at time.Time, kind, severity, title string, meta map[string]string) {
	e := store.Event{TS: at, Kind: kind, Severity: severity, Title: title}
	if len(meta) > 0 {
		if b, err := json.Marshal(meta); err == nil {
			e.Meta = b
		}
	}
	p.enqueue(queued{event: &e})
}

func (p *Pipeline) enqueue(q queued) {
	select {
	case p.queue <- q:
		if len(p.queue) >= p.opts.FlushLines {
			select {
			case p.flush <- struct{}{}:
			default:
			}
		}
	default:
		p.dropMu.Lock()
		p.dropped++
		n := p.dropped
		p.dropMu.Unlock()
		if n%warnEveryDrops == 1 {
			p.log.Warn("dropping router log lines, the store is not keeping up", "dropped_total", n)
		}
	}
}

// Run writes batches until ctx ends, then writes what is still queued. Stop the
// router first, so that its last lines are in the queue.
func (p *Pipeline) Run(ctx context.Context) {
	ticker := time.NewTicker(p.opts.FlushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.drain()
			return
		case <-ticker.C:
		case <-p.flush:
		}
		p.drain()
	}
}

// drain writes everything queued now, in batches.
func (p *Pipeline) drain() {
	for {
		var lines []store.LogLine
		var events []store.Event
	collect:
		for len(lines)+len(events) < p.opts.FlushLines {
			select {
			case q := <-p.queue:
				if q.line != nil {
					lines = append(lines, *q.line)
				} else {
					events = append(events, *q.event)
				}
			default:
				break collect
			}
		}
		if len(lines)+len(events) == 0 {
			return
		}
		p.write(lines, events)
	}
}

func (p *Pipeline) write(lines []store.LogLine, events []store.Event) {
	// Not tied to Run's context: the final drain happens after it has ended.
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.opts.Store.InsertLogLines(ctx, lines); err != nil {
		p.log.Warn("storing router log lines failed", "lines", len(lines), "error", err)
	}
	for i := range events {
		id, err := p.opts.Store.InsertEvent(ctx, events[i])
		if err != nil {
			p.log.Warn("storing event failed", "kind", events[i].Kind, "error", err)
		}
		events[i].ID = id
	}
	for i := range lines {
		p.publish(Item{Line: &lines[i]})
	}
	for i := range events {
		p.publish(Item{Event: &events[i]})
	}
}

func (p *Pipeline) publish(it Item) {
	for id, ch := range p.subs {
		select {
		case ch <- it:
		default:
			// A reader this far behind reconnects and gets the backlog.
			close(ch)
			delete(p.subs, id)
		}
	}
}

// Tail returns the newest lines matching the filter, oldest first, and a channel
// with every line and event stored after them. The channel closes if the reader
// falls too far behind. The function ends the subscription.
func (p *Pipeline) Tail(ctx context.Context, f store.LogFilter) ([]store.LogLine, <-chan Item, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	lines, _, err := p.opts.Store.ListLogLines(ctx, f)
	if err != nil {
		return nil, nil, nil, err
	}
	backlog := make([]store.LogLine, len(lines))
	for i, l := range lines {
		backlog[len(lines)-1-i] = l
	}
	id := p.nextSub
	p.nextSub++
	ch := make(chan Item, subscriberBuffer)
	p.subs[id] = ch
	return backlog, ch, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if _, ok := p.subs[id]; ok {
			delete(p.subs, id)
			close(ch)
		}
	}, nil
}
