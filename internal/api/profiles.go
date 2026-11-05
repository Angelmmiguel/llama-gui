package api

import (
	"net/http"

	"github.com/Angelmmiguel/llama-gui/internal/profiles"
)

func (a *api) listProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := a.Profiles.List(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	if list == nil {
		list = []profiles.Profile{}
	}
	skipped := a.Profiles.Skipped()
	if skipped == nil {
		skipped = []profiles.Skipped{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"profiles": list, "skipped": skipped})
}

func (a *api) createProfile(w http.ResponseWriter, r *http.Request) {
	var p profiles.Profile
	if !a.decodeJSON(w, r, &p, false) {
		return
	}
	created, err := a.Profiles.Create(r.Context(), p)
	if err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, created)
}

func (a *api) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := a.Profiles.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, p)
}

func (a *api) updateProfile(w http.ResponseWriter, r *http.Request) {
	var p profiles.Profile
	if !a.decodeJSON(w, r, &p, false) {
		return
	}
	updated, err := a.Profiles.Update(r.Context(), r.PathValue("id"), p)
	if err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, updated)
}

func (a *api) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := a.Profiles.Delete(r.Context(), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) profileCommand(w http.ResponseWriter, r *http.Request) {
	p, err := a.Profiles.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"command": a.Profiles.Command(p)})
}
