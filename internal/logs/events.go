package logs

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Event kinds.
const (
	EventRouterStarted   = "router.started"
	EventRouterListening = "router.listening"
	EventModelLoading    = "model.loading"
	EventModelLoaded     = "model.loaded"
	EventInstanceExited  = "instance.exited"
	EventContextExceeded = "slot.context_exceeded"
	EventQueueFull       = "queue.full"
	EventDeferred        = "request.deferred"
)

// Event severities.
const (
	SeverityInfo  = "info"
	SeverityWarn  = "warn"
	SeverityError = "error"
)

// Event is something worth surfacing that a log line implies.
type Event struct {
	Kind     string
	Severity string
	Title    string
	Meta     map[string]string
}

// Detector derives events from lines. It remembers which instances were spawned to
// serve, because the router also starts short-lived ones to measure memory, whose
// clean exit means nothing. Safe for concurrent use.
type Detector struct {
	mu      sync.Mutex
	serving map[string]bool
}

var (
	spawnRe  = regexp.MustCompile(`spawning server instance with name=(\S+) on port (\d+)`)
	exitRe   = regexp.MustCompile(`instance name=(\S+) exited with status (-?\d+)`)
	listenRe = regexp.MustCompile(`listening on (\S+)`)
)

// Observe returns the events a line implies; unknown lines yield none.
func (d *Detector) Observe(l Line) []Event {
	m := l.Message
	lower := strings.ToLower(m)
	switch {
	case l.Port == 0 && strings.Contains(m, "starting server in router mode"):
		return []Event{{Kind: EventRouterStarted, Severity: SeverityInfo, Title: "Router started"}}
	case l.Port == 0 && l.Source != SourceOther && listenRe.MatchString(m):
		addr := listenRe.FindStringSubmatch(m)[1]
		return []Event{{Kind: EventRouterListening, Severity: SeverityInfo, Title: "Router listening on " + addr, Meta: map[string]string{"addr": addr}}}
	}
	if sm := spawnRe.FindStringSubmatch(m); sm != nil {
		d.mu.Lock()
		if d.serving == nil {
			d.serving = map[string]bool{}
		}
		d.serving[sm[1]] = true
		d.mu.Unlock()
		return []Event{{Kind: EventModelLoading, Severity: SeverityInfo, Title: "Loading " + sm[1], Meta: map[string]string{"model": sm[1], "port": sm[2]}}}
	}
	if em := exitRe.FindStringSubmatch(m); em != nil {
		d.mu.Lock()
		serving := d.serving[em[1]]
		delete(d.serving, em[1])
		d.mu.Unlock()
		if em[2] == "0" && !serving {
			return nil
		}
		sev, title := SeverityInfo, "Unloaded "+em[1]
		if em[2] != "0" {
			sev, title = SeverityError, em[1]+" exited with status "+em[2]
		}
		return []Event{{Kind: EventInstanceExited, Severity: sev, Title: title, Meta: map[string]string{"model": em[1], "status": em[2]}}}
	}
	switch {
	case l.Port > 0 && strings.Contains(m, "server_run_i: model loaded"):
		return []Event{{Kind: EventModelLoaded, Severity: SeverityInfo, Title: "Loaded " + l.Model, Meta: modelMeta(l)}}
	case strings.Contains(lower, "exceeds the available context size") || strings.Contains(lower, "context size has been exceeded"):
		return []Event{{Kind: EventContextExceeded, Severity: SeverityWarn, Title: "Context size exceeded", Meta: modelMeta(l)}}
	case strings.Contains(lower, "queue is full") || strings.Contains(lower, "queue full"):
		return []Event{{Kind: EventQueueFull, Severity: SeverityWarn, Title: "Request queue full", Meta: modelMeta(l)}}
	case strings.Contains(lower, "deferring") || strings.Contains(lower, "deferred"):
		return []Event{{Kind: EventDeferred, Severity: SeverityInfo, Title: "Request deferred", Meta: modelMeta(l)}}
	}
	return nil
}

func modelMeta(l Line) map[string]string {
	meta := map[string]string{}
	if l.Model != "" {
		meta["model"] = l.Model
	}
	if l.Port > 0 {
		meta["port"] = strconv.Itoa(l.Port)
	}
	return meta
}
