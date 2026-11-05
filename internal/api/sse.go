package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// sse writes a Server-Sent Events response. The server sets no WriteTimeout, so
// these can stay open for as long as the client wants.
type sse struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func startSSE(w http.ResponseWriter) *sse {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sse{w: w, rc: http.NewResponseController(w)}
	_ = s.rc.Flush()
	return s
}

// send writes one message. The event name and id are left out when empty.
func (s *sse) send(event string, id int64, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if event != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if id != 0 {
		if _, err := fmt.Fprintf(s.w, "id: %d\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *sse) ping() error {
	if _, err := fmt.Fprint(s.w, ": ping\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}
