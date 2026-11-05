package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Angelmmiguel/llama-gui/internal/bench"
)

// historyRuns is how many of a model's runs its history looks through.
const historyRuns = 200

// Bench runs llama-bench and remembers the results.
type Bench interface {
	Start(ctx context.Context, setup bench.Setup) (bench.Run, error)
	Current() (bench.Run, bool)
	Cancel() error
	List(ctx context.Context, model string, limit int) ([]bench.Run, error)
	Get(ctx context.Context, id int64) (bench.Run, error)
}

// benchRun is a run with what the page derives from it. A takeaway waits for the
// run to end, so it never compares configurations that are still being measured.
type benchRun struct {
	bench.Run
	Series   bench.Charts `json:"series"`
	Takeaway string       `json:"takeaway,omitempty"`
	Markdown string       `json:"markdown"`
}

func describeRun(run bench.Run) benchRun {
	if run.Results == nil {
		run.Results = []bench.Result{}
	}
	out := benchRun{Run: run, Series: bench.Series(run), Markdown: bench.Markdown(run)}
	if out.Series.Decode == nil {
		out.Series.Decode = []bench.Line{}
	}
	if out.Series.Prefill == nil {
		out.Series.Prefill = []bench.Line{}
	}
	if run.State != bench.StateRunning {
		out.Takeaway = bench.Takeaway(run)
	}
	return out
}

func (a *api) listBenchRuns(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	limit := q.integer("limit", 50, 1, historyRuns)
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	runs, err := a.Bench.List(r.Context(), q.str("model"), limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	if runs == nil {
		runs = []bench.Run{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (a *api) getBenchRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid run id")
		return
	}
	// The store only learns a run's results when it ends.
	if run, ok := a.Bench.Current(); ok && run.ID == id && run.State == bench.StateRunning {
		WriteJSON(w, http.StatusOK, describeRun(run))
		return
	}
	run, err := a.Bench.Get(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, describeRun(run))
}

func (a *api) startBenchRun(w http.ResponseWriter, r *http.Request) {
	var setup bench.Setup
	if !a.decodeJSON(w, r, &setup, false) {
		return
	}
	run, err := a.Bench.Start(r.Context(), setup)
	if err != nil {
		a.fail(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, describeRun(run))
}

func (a *api) currentBenchRun(w http.ResponseWriter, _ *http.Request) {
	run, ok := a.Bench.Current()
	if !ok {
		WriteJSON(w, http.StatusOK, map[string]any{"run": nil})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"run": describeRun(run)})
}

func (a *api) cancelBenchRun(w http.ResponseWriter, _ *http.Request) {
	if err := a.Bench.Cancel(); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) estimateBench(w http.ResponseWriter, r *http.Request) {
	var setup bench.Setup
	if !a.decodeJSON(w, r, &setup, false) {
		return
	}
	if err := setup.Validate(); err != nil {
		a.fail(w, err)
		return
	}
	est := setup.Estimate()
	WriteJSON(w, http.StatusOK, map[string]any{"tests": est.Tests, "seconds": est.Seconds, "command": setup.Command()})
}

func (a *api) benchHistory(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	if model == "" {
		badRequest(w, "model: required")
		return
	}
	runs, err := a.Bench.List(r.Context(), model, historyRuns)
	if err != nil {
		a.fail(w, err)
		return
	}
	points := bench.History(runs, model)
	if points == nil {
		points = []bench.BuildPoint{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"history": points})
}
