package api

import "net/http"

func (a *api) telemetry(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, a.Telemetry.Snapshot())
}

func (a *api) streamTelemetry(w http.ResponseWriter, r *http.Request) {
	ch, cancel := a.Telemetry.Subscribe()
	defer cancel()
	stream := startSSE(w)
	if stream.send("", 0, a.Telemetry.Snapshot()) != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.Done:
			return
		case snap := <-ch:
			if stream.send("", 0, snap) != nil {
				return
			}
		}
	}
}
