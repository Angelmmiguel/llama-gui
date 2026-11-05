package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

const defaultReleaseLimit = 10

type buildJSON struct {
	builds.Manifest
	Active bool `json:"active"`
	// Supported says the build can run as a router.
	Supported bool `json:"supported"`
}

func (a *api) listBuilds(w http.ResponseWriter, r *http.Request) {
	list, err := a.Builds.List()
	if err != nil {
		a.fail(w, err)
		return
	}
	active, err := a.Builds.Active(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]buildJSON, len(list))
	for i, m := range list {
		out[i] = buildJSON{Manifest: m, Active: m.ID == active, Supported: m.Probe.Router}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"builds": out, "active": active})
}

type availableRelease struct {
	Tag         string           `json:"tag"`
	BinaryTag   string           `json:"binary_tag,omitempty"`
	Channel     releases.Channel `json:"channel"`
	PublishedAt time.Time        `json:"published_at"`
	Body        string           `json:"body"`
	// Options is only filled in for the newest release of a channel and for an exact
	// version: listing a stable release's backends takes another download per release.
	Options []builds.Option `json:"options,omitempty"`
}

func toAvailable(rel releases.Release) availableRelease {
	return availableRelease{Tag: rel.Tag, BinaryTag: rel.BinaryTag, Channel: rel.Channel, PublishedAt: rel.PublishedAt, Body: rel.Body}
}

func (a *api) availableBuilds(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	limit := q.integer("limit", defaultReleaseLimit, 1, 100)
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	ctx := r.Context()
	arch := builds.HostArch()
	withOptions := func(version string) (availableRelease, error) {
		rel, err := a.Releases.ResolveVersion(ctx, version)
		if err != nil {
			return availableRelease{}, err
		}
		opts, err := a.Builds.Available(ctx, rel, arch)
		if err != nil {
			return availableRelease{}, err
		}
		out := toAvailable(rel)
		out.Options = opts
		return out, nil
	}

	if version := q.str("version"); version != "" {
		rel, err := withOptions(version)
		if err != nil {
			a.fail(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"releases": []availableRelease{rel}})
		return
	}

	var channels []releases.Channel
	switch c := q.str("channel"); c {
	case "":
		channels = []releases.Channel{releases.ChannelStable, releases.ChannelNightly}
	case string(releases.ChannelStable), string(releases.ChannelNightly):
		channels = []releases.Channel{releases.Channel(c)}
	default:
		badRequest(w, "channel: %q is not stable or nightly", c)
		return
	}
	out := []availableRelease{}
	for _, channel := range channels {
		list, err := a.Releases.List(ctx, channel, limit)
		if err != nil {
			a.fail(w, err)
			return
		}
		for i, rel := range list {
			entry := toAvailable(rel)
			if i == 0 {
				// A failure here only costs the backends of a list that is still useful.
				if resolved, err := withOptions(rel.Tag); err == nil {
					entry = resolved
				} else {
					a.log.Warn("resolving release", "tag", rel.Tag, "error", err)
				}
			}
			out = append(out, entry)
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"releases": out})
}

// checkBuilds looks for new releases at the user's request. It never installs anything,
// whatever the update policy.
func (a *api) checkBuilds(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, a.CheckUpdates(r.Context()))
}

func (a *api) compareBuilds(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	from, to := q.str("from"), q.str("to")
	for _, tag := range []string{from, to} {
		if err := releases.ValidateTag(tag); err != nil {
			a.fail(w, err)
			return
		}
	}
	cmp, err := a.Releases.Compare(r.Context(), from, to)
	if err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, cmp)
}

func (a *api) installBuild(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
		Backend string `json:"backend"`
	}
	if !a.decodeJSON(w, r, &body, false) {
		return
	}
	if body.Version == "" {
		badRequest(w, "version is required")
		return
	}
	if body.Backend == "" {
		body.Backend = builds.AutoBackend
	}
	job, err := a.Builds.Install(r.Context(), body.Version, body.Backend)
	switch {
	case err == nil, errors.Is(err, builds.ErrJobActive):
		WriteJSON(w, http.StatusAccepted, job)
	case errors.Is(err, builds.ErrAlreadyInstalled):
		WriteJSON(w, http.StatusOK, job)
	default:
		a.fail(w, err)
	}
}

func (a *api) listJobs(w http.ResponseWriter, _ *http.Request) {
	jobs := a.Builds.Jobs()
	if jobs == nil {
		jobs = []builds.Job{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (a *api) cancelJob(w http.ResponseWriter, r *http.Request) {
	if err := a.Builds.Cancel(r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) activateBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Builds.Activate(r.Context(), id); err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, map[string]string{"active": id})
}

func (a *api) removeBuild(w http.ResponseWriter, r *http.Request) {
	if err := a.Builds.Remove(r.Context(), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
