package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"

	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
)

type modelJSON struct {
	ID            string       `json:"id"`
	Status        string       `json:"status"`
	Port          int          `json:"port,omitempty"`
	ExitCode      int          `json:"exit_code,omitempty"`
	Failed        bool         `json:"failed,omitempty"`
	ContextLength int          `json:"context_length,omitempty"`
	Source        string       `json:"source,omitempty"`
	Memory        llama.Memory `json:"memory"`
}

func (a *api) router(ctx context.Context) (Router, context.Context, context.CancelFunc, error) {
	router, up := a.Router()
	if !up {
		return nil, nil, nil, errNotRunning
	}
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	return router, ctx, cancel, nil
}

func (a *api) listModels(w http.ResponseWriter, r *http.Request) {
	out := []modelJSON{}
	router, ctx, cancel, err := a.router(r.Context())
	if err != nil {
		// With the router down there are no models to list, which is not an error.
		WriteJSON(w, http.StatusOK, map[string]any{"models": out})
		return
	}
	defer cancel()
	models, err := router.Models(ctx)
	if err != nil {
		a.fail(w, err)
		return
	}
	for _, m := range models {
		out = append(out, modelJSON{
			ID: m.ID, Status: m.Status, Port: m.Port, ExitCode: m.ExitCode, Failed: m.Failed,
			ContextLength: m.ContextLength, Source: m.Source, Memory: m.Memory,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"models": out})
}

func (a *api) loadModel(w http.ResponseWriter, r *http.Request) {
	a.modelAction(w, r, Router.Load)
}

func (a *api) unloadModel(w http.ResponseWriter, r *http.Request) {
	a.modelAction(w, r, Router.Unload)
}

func (a *api) modelAction(w http.ResponseWriter, r *http.Request, op func(Router, context.Context, string) error) {
	id := r.PathValue("id")
	if !modelIDRe.MatchString(id) {
		badRequest(w, "invalid model id")
		return
	}
	router, ctx, cancel, err := a.router(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	defer cancel()
	if err := op(router, ctx, id); err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

// listSlots answers from the sampler's last tick: it already has every slot, and
// asking the router again would add load for data one second fresher.
func (a *api) listSlots(w http.ResponseWriter, _ *http.Request) {
	snap := a.Telemetry.Snapshot()
	out := []telemetry.Model{}
	for _, m := range snap.Models {
		if len(m.Slots) > 0 {
			out = append(out, m)
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"models": out, "in_flight": snap.InFlight, "time": snap.Time})
}

// slotFileRe is deliberately narrow: the name becomes a file in the slot save
// directory, on the router's side of the fence.
var slotFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (a *api) slotAction(w http.ResponseWriter, r *http.Request) {
	model := r.PathValue("model")
	if !modelIDRe.MatchString(model) {
		badRequest(w, "invalid model id")
		return
	}
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil || slot < 0 {
		badRequest(w, "invalid slot %q", r.PathValue("slot"))
		return
	}
	action := r.PathValue("action")
	switch action {
	case "cancel":
		if !a.CancelSlot(model, slot) {
			WriteError(w, http.StatusNotFound, "no request is using this slot")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"cancelled": true})
		return
	case "save", "restore", "erase":
	default:
		badRequest(w, "action: %q is not save, restore, erase or cancel", action)
		return
	}
	var body struct {
		Filename string `json:"filename"`
	}
	if !a.decodeJSON(w, r, &body, true) {
		return
	}
	if action == "erase" {
		body.Filename = ""
	} else if !slotFileRe.MatchString(body.Filename) {
		badRequest(w, "filename: must match %s", slotFileRe)
		return
	}
	router, ctx, cancel, err := a.router(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	defer cancel()
	out, err := router.SlotAction(ctx, model, slot, action, body.Filename)
	if err != nil {
		a.fail(w, err)
		return
	}
	if out == nil {
		out = map[string]any{}
	}
	WriteJSON(w, http.StatusOK, out)
}
