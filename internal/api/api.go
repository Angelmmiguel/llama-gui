// Package api serves the /api/v1 management API.
package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// Deps is what the handlers need from the rest of the service.
type Deps struct {
	Version string
	// Capture is the capture mode: meta or bodies.
	Capture string
	Started time.Time
	Now     func() time.Time
	// State reports the router's state.
	State func() string
}

// Handler returns the API. Unknown routes and methods answer in the API's JSON
// error shape rather than the mux's plain text.
func Handler(deps Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/healthz", healthz)
	mux.HandleFunc("GET /api/v1/status", status(deps))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern == "" {
			h.ServeHTTP(&jsonErrorWriter{ResponseWriter: w}, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// statusResponse is the body of GET /status.
type statusResponse struct {
	State         string `json:"state"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Capture       string `json:"capture"`
}

func status(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, statusResponse{
			State:         deps.State(),
			Version:       deps.Version,
			UptimeSeconds: int64(deps.Now().Sub(deps.Started).Seconds()),
			Capture:       deps.Capture,
		})
	}
}

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "could not encode the response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// WriteError writes the API's error shape, {"error": message}.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}

// jsonErrorWriter turns the mux's own 404 and 405 into JSON, keeping headers
// such as Allow that it set first.
type jsonErrorWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *jsonErrorWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	WriteError(w.ResponseWriter, status, http.StatusText(status))
}

func (w *jsonErrorWriter) Write(b []byte) (int, error) { return len(b), nil }
