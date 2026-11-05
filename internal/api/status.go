package api

import "net/http"

// statusResponse is the body of GET /status.
type statusResponse struct {
	State         string `json:"state"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Capture       string `json:"capture"`
}

func (a *api) status(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, statusResponse{
		State:         a.State(),
		Version:       a.Version,
		UptimeSeconds: int64(a.Now().Sub(a.Started).Seconds()),
		Capture:       a.Capture,
	})
}
