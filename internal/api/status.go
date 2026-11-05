package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/supervisor"
)

// stopTimeout covers the supervisor's way to SIGKILL.
const stopTimeout = 15 * time.Second

type activeBuild struct {
	ID      string `json:"id"`
	Tag     string `json:"tag"`
	Backend string `json:"backend"`
}

// statusResponse is the body of GET /status.
type statusResponse struct {
	State         string `json:"state"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Capture       string `json:"capture"`
	// RouterSince is when the router entered its current state.
	RouterSince     time.Time         `json:"router_since"`
	PID             int               `json:"pid,omitempty"`
	RestartCount    int               `json:"restart_count"`
	LastError       string            `json:"last_error,omitempty"`
	ActiveBuild     *activeBuild      `json:"active_build"`
	ModelsLoaded    int               `json:"models_loaded"`
	UpdateAvailable bool              `json:"update_available"`
	Update          builds.UpdateInfo `json:"update"`
}

func (a *api) status(w http.ResponseWriter, r *http.Request) {
	st := a.Server.Status()
	resp := statusResponse{
		State:         string(st.State),
		Version:       a.Version,
		UptimeSeconds: int64(a.Now().Sub(a.Started).Seconds()),
		Capture:       a.Capture,
		RouterSince:   st.Since,
		PID:           st.PID,
		RestartCount:  st.RestartCount,
		LastError:     st.LastError,
		Update:        a.Updates(r.Context()),
	}
	resp.UpdateAvailable = resp.Update.Available
	if id, err := a.Builds.Active(r.Context()); err == nil && id != "" {
		if man, err := a.Builds.Get(id); err == nil {
			resp.ActiveBuild = &activeBuild{ID: man.ID, Tag: man.Tag, Backend: man.Backend}
		}
	}
	for _, m := range a.Telemetry.Snapshot().Models {
		if m.Status == "loaded" {
			resp.ModelsLoaded++
		}
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (a *api) hardware(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, a.Hardware(r.Context()))
}

func (a *api) serverStart(w http.ResponseWriter, r *http.Request) {
	a.control(w, r, a.Server.Start, http.StatusAccepted)
}

func (a *api) serverRestart(w http.ResponseWriter, r *http.Request) {
	a.control(w, r, a.Server.Restart, http.StatusAccepted)
}

func (a *api) serverStop(w http.ResponseWriter, r *http.Request) {
	// The stop goes through even if the client hangs up: half-stopping
	// would be the one bad outcome, and ctx ending escalates to SIGKILL.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), stopTimeout)
	defer cancel()
	a.control(w, r.WithContext(ctx), a.Server.Stop, http.StatusOK)
}

func (a *api) control(w http.ResponseWriter, r *http.Request, op func(context.Context) error, ok int) {
	if err := op(r.Context()); err != nil {
		a.fail(w, err)
		return
	}
	st := a.Server.Status()
	WriteJSON(w, ok, map[string]supervisor.State{"state": st.State})
}
