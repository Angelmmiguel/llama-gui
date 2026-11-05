package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

func TestListBuilds(t *testing.T) {
	h := newHarness(t)
	h.builds.list = []builds.Manifest{
		{ID: "b2-cpu", Tag: "b2", Probe: builds.Probe{Router: true}},
		{ID: "b1-cpu", Tag: "b1"},
	}
	h.builds.active = "b2-cpu"
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/builds", "")
	list, _ := body["builds"].([]any)
	if code != http.StatusOK || len(list) != 2 || body["active"] != "b2-cpu" {
		t.Fatalf("%d %v", code, body)
	}
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if first["id"] != "b2-cpu" || first["active"] != true || first["supported"] != true || second["active"] != false || second["supported"] != false {
		t.Errorf("%v %v", first, second)
	}
}

func TestInstallBuild(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		body   string
		status int
		call   string
	}{
		{"started", nil, `{"version":"stable","backend":"cpu"}`, http.StatusAccepted, "install stable cpu"},
		{"auto backend by default", nil, `{"version":"b1"}`, http.StatusAccepted, "install b1 auto"},
		{"being installed", builds.ErrJobActive, `{"version":"b1"}`, http.StatusAccepted, "install b1 auto"},
		{"already there", builds.ErrAlreadyInstalled, `{"version":"b1"}`, http.StatusOK, "install b1 auto"},
		{"bad tag", releases.ErrInvalidTag, `{"version":"x"}`, http.StatusBadRequest, "install x auto"},
		{"bad backend", builds.ErrInvalidBackend, `{"version":"b1","backend":"x"}`, http.StatusBadRequest, "install b1 x"},
		{"no asset", builds.ErrNoAsset, `{"version":"b1"}`, http.StatusNotFound, "install b1 auto"},
		{"rate limited", &releases.RateLimitError{Reset: testNow.Add(time.Minute)}, `{"version":"b1"}`, http.StatusServiceUnavailable, "install b1 auto"},
		{"no version", nil, `{}`, http.StatusBadRequest, ""},
		{"unknown field", nil, `{"version":"b1","x":1}`, http.StatusBadRequest, ""},
		{"not json", nil, `nope`, http.StatusBadRequest, ""},
		{"no body", nil, ``, http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.builds.job = builds.Job{ID: "b1-cpu", State: builds.StateQueued}
			h.builds.installErr = tc.err
			w := h.send(t, http.MethodPost, "/api/v1/builds/install", tc.body)
			if w.Code != tc.status {
				t.Errorf("status %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.call != "" && (len(h.builds.calls) != 1 || h.builds.calls[0] != tc.call) {
				t.Errorf("calls %v", h.builds.calls)
			}
			if tc.name == "rate limited" && w.Header().Get("Retry-After") != "60" {
				t.Errorf("Retry-After = %q", w.Header().Get("Retry-After"))
			}
		})
	}
}

func TestBuildActions(t *testing.T) {
	h := newHarness(t)
	h.builds.jobs = []builds.Job{{ID: "b1-cpu"}}
	h.table(t, []route{
		{"jobs", http.MethodGet, "/api/v1/builds/jobs", "", http.StatusOK, ""},
		{"activate", http.MethodPost, "/api/v1/builds/b1-cpu/activate", "", http.StatusAccepted, ""},
		{"remove", http.MethodDelete, "/api/v1/builds/b1-cpu", "", http.StatusNoContent, ""},
		{"cancel job", http.MethodDelete, "/api/v1/builds/jobs/b1-cpu", "", http.StatusNoContent, ""},
	})
	for _, tc := range []struct {
		err    error
		status int
	}{
		{builds.ErrInvalidID, http.StatusBadRequest},
		{builds.ErrNotFound, http.StatusNotFound},
		{builds.ErrActiveBuild, http.StatusConflict},
		{builds.ErrUnsupportedBuild, http.StatusConflict},
	} {
		h.builds.err = tc.err
		h.table(t, []route{
			{tc.err.Error() + " activating", http.MethodPost, "/api/v1/builds/x/activate", "", tc.status, tc.err.Error()},
			{tc.err.Error() + " removing", http.MethodDelete, "/api/v1/builds/x", "", tc.status, tc.err.Error()},
		})
	}
	h.builds.err = builds.ErrJobNotFound
	h.table(t, []route{{"unknown job", http.MethodDelete, "/api/v1/builds/jobs/x", "", http.StatusNotFound, ""}})
}

func TestAvailableBuilds(t *testing.T) {
	h := newHarness(t)
	h.releases.list[releases.ChannelStable] = []releases.Release{{Tag: "v2"}, {Tag: "v1"}}
	h.releases.list[releases.ChannelNightly] = []releases.Release{{Tag: "b9"}}

	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/builds/available", "")
	list, _ := body["releases"].([]any)
	if code != http.StatusOK || len(list) != 3 {
		t.Fatalf("%d %v", code, body)
	}
	newest, older := list[0].(map[string]any), list[1].(map[string]any)
	if newest["tag"] != "v2" || newest["options"] == nil || older["options"] != nil {
		t.Errorf("only the newest has options: %v %v", newest, older)
	}

	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/builds/available?channel=nightly", "")
	if list, _ := body["releases"].([]any); len(list) != 1 {
		t.Errorf("channel filter: %v", body)
	}
	_, body = h.sendJSON(t, http.MethodGet, "/api/v1/builds/available?version=b5", "")
	if list, _ := body["releases"].([]any); len(list) != 1 || list[0].(map[string]any)["options"] == nil {
		t.Errorf("exact version: %v", body)
	}

	h.table(t, []route{
		{"bad channel", http.MethodGet, "/api/v1/builds/available?channel=x", "", http.StatusBadRequest, "channel"},
		{"bad limit", http.MethodGet, "/api/v1/builds/available?limit=0", "", http.StatusBadRequest, "limit"},
	})

	h.releases.resolve = releases.ErrNotFound
	h.table(t, []route{
		{"unknown version", http.MethodGet, "/api/v1/builds/available?version=b5", "", http.StatusNotFound, ""},
		{"list survives a resolve failure", http.MethodGet, "/api/v1/builds/available", "", http.StatusOK, ""},
	})
	h.releases.err = &releases.RateLimitError{}
	h.table(t, []route{{"rate limited", http.MethodGet, "/api/v1/builds/available", "", http.StatusServiceUnavailable, ""}})
}

func TestCompareBuilds(t *testing.T) {
	h := newHarness(t)
	h.releases.cmp = releases.Comparison{AheadBy: 2, Commits: []releases.Commit{{SHA: "a", Title: "x"}}}
	code, body := h.sendJSON(t, http.MethodGet, "/api/v1/builds/compare?from=b1&to=b2", "")
	if code != http.StatusOK || body["ahead_by"] != 2.0 {
		t.Errorf("%d %v", code, body)
	}
	h.table(t, []route{
		{"missing", http.MethodGet, "/api/v1/builds/compare?from=b1", "", http.StatusBadRequest, ""},
		{"traversal", http.MethodGet, "/api/v1/builds/compare?from=../x&to=b1", "", http.StatusBadRequest, ""},
	})
}

func TestCheckBuilds(t *testing.T) {
	h := newHarness(t)
	code, body := h.sendJSON(t, http.MethodPost, "/api/v1/builds/check", "")
	if code != http.StatusOK || body["latest"] != "v3" || h.checks != 1 {
		t.Errorf("%d %v after %d checks", code, body, h.checks)
	}
}
