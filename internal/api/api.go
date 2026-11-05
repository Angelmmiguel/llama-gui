// Package api serves the /api/v1 management API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/builds"
	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/logs"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
	"github.com/Angelmmiguel/llama-gui/internal/store"
	"github.com/Angelmmiguel/llama-gui/internal/supervisor"
	"github.com/Angelmmiguel/llama-gui/internal/telemetry"
)

// upstreamTimeout bounds calls to the router made on behalf of a request.
const upstreamTimeout = 30 * time.Second

const maxJSONBody = 1 << 20

// Server controls the router. Start and Restart return once the work is under
// way, with errors only for what can be known at once, such as having no build.
type Server interface {
	Status() supervisor.Status
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
}

// Builds is the installed builds. Install takes the backend "auto" too; Activate
// also restarts the router on the new build, in the background.
type Builds interface {
	List() ([]builds.Manifest, error)
	Active(ctx context.Context) (string, error)
	Get(id string) (builds.Manifest, error)
	Jobs() []builds.Job
	Install(ctx context.Context, version, backend string) (builds.Job, error)
	Cancel(id string) error
	Activate(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Available(ctx context.Context, rel releases.Release, arch string) ([]builds.Option, error)
}

// Releases reads what upstream has published.
type Releases interface {
	List(ctx context.Context, channel releases.Channel, limit int) ([]releases.Release, error)
	ResolveVersion(ctx context.Context, version string) (releases.Release, error)
	Compare(ctx context.Context, base, head string) (releases.Comparison, error)
}

// Profiles is CRUD over launch profiles. Changes reach a running router.
type Profiles interface {
	List(ctx context.Context) ([]profiles.Profile, error)
	Get(ctx context.Context, id string) (profiles.Profile, error)
	Create(ctx context.Context, p profiles.Profile) (profiles.Profile, error)
	Update(ctx context.Context, id string, p profiles.Profile) (profiles.Profile, error)
	Delete(ctx context.Context, id string) error
	Command(p profiles.Profile) string
	Skipped() []profiles.Skipped
}

// Router is the part of the router's API that the handlers call.
type Router interface {
	Models(ctx context.Context) ([]llama.Model, error)
	Load(ctx context.Context, id string) error
	Unload(ctx context.Context, id string) error
	SlotAction(ctx context.Context, model string, slot int, action, filename string) (map[string]any, error)
}

// Store is the part of internal/store behind the history routes.
type Store interface {
	ListRequests(ctx context.Context, f store.RequestFilter) ([]store.Request, string, error)
	RequestsForSummary(ctx context.Context, f store.RequestFilter, maxRows int) ([]store.Request, error)
	GetRequest(ctx context.Context, id int64) (store.Request, error)
	GetRequestBody(ctx context.Context, requestID int64) (request, response []byte, err error)
	ListLogLines(ctx context.Context, f store.LogFilter) ([]store.LogLine, string, error)
	LogLinesAfter(ctx context.Context, f store.LogFilter, afterID int64) ([]store.LogLine, error)
	LogVolume(ctx context.Context, since time.Time) ([]store.LogVolumeBucket, error)
	CountLogLevels(ctx context.Context, since time.Time) (map[string]int64, error)
	ListEvents(ctx context.Context, since time.Time) ([]store.Event, error)
}

// Telemetry is the live sample and its stream.
type Telemetry interface {
	Snapshot() *telemetry.Snapshot
	Subscribe() (<-chan *telemetry.Snapshot, func())
}

// Tailer follows the log as it is written.
type Tailer interface {
	Tail(ctx context.Context, f store.LogFilter) ([]store.LogLine, <-chan logs.Item, func(), error)
}

// Deps is what the handlers need from the rest of the service.
type Deps struct {
	Version string
	// Capture is the capture mode: meta or bodies.
	Capture string
	Started time.Time
	Now     func() time.Time
	Logger  *slog.Logger

	Server    Server
	Builds    Builds
	Releases  Releases
	Profiles  Profiles
	Store     Store
	Telemetry Telemetry
	Tail      Tailer
	// Router returns the router's client, or false while it is not running.
	Router func() (Router, bool)
	// CancelSlot closes the request occupying a slot, reporting whether there was one.
	CancelSlot func(model string, slot int) bool
	Hardware   func(ctx context.Context) hardware.Snapshot
	Updates    func(ctx context.Context) builds.UpdateInfo
	// Done closes as shutdown begins, ending the streams so they don't hold up the drain.
	Done <-chan struct{}

	// State reports the router's state.
	State func() string
}

type api struct {
	Deps
	log *slog.Logger
}

// Handler returns the API. Unknown routes and methods answer in the API's JSON
// error shape rather than the mux's plain text.
func Handler(deps Deps) http.Handler {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	a := &api{Deps: deps, log: deps.Logger}
	if a.log == nil {
		a.log = slog.Default()
	}

	mux := http.NewServeMux()
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /healthz": healthz,
		"GET /status":  a.status,

		"GET /builds":                a.listBuilds,
		"GET /builds/available":      a.availableBuilds,
		"GET /builds/compare":        a.compareBuilds,
		"POST /builds/install":       a.installBuild,
		"GET /builds/jobs":           a.listJobs,
		"DELETE /builds/jobs/{id}":   a.cancelJob,
		"POST /builds/{id}/activate": a.activateBuild,
		"DELETE /builds/{id}":        a.removeBuild,

		"GET /profiles":              a.listProfiles,
		"POST /profiles":             a.createProfile,
		"GET /profiles/{id}":         a.getProfile,
		"PUT /profiles/{id}":         a.updateProfile,
		"DELETE /profiles/{id}":      a.deleteProfile,
		"GET /profiles/{id}/command": a.profileCommand,

		"GET /models":              a.listModels,
		"POST /models/{id}/load":   a.loadModel,
		"POST /models/{id}/unload": a.unloadModel,

		"GET /slots":                          a.listSlots,
		"POST /slots/{model}/{slot}/{action}": a.slotAction,

		"GET /requests":         a.listRequests,
		"GET /requests/summary": a.requestSummary,
		"GET /requests/{id}":    a.getRequest,

		"GET /logs":          a.listLogs,
		"GET /logs/stream":   a.streamLogs,
		"GET /logs/volume":   a.logVolume,
		"GET /logs/download": a.downloadLogs,
		"GET /events":        a.listEvents,
	} {
		method, path, _ := strings.Cut(pattern, " ")
		mux.HandleFunc(method+" /api/v1"+path, h)
	}

	return sameOriginWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern == "" {
			h.ServeHTTP(&jsonErrorWriter{ResponseWriter: w}, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

// sameOriginWrites stops other sites from driving the API through a visitor's
// browser, which matters because there is no authentication to stop them.
// Non-browser clients don't send the header and are let through.
func sameOriginWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			switch r.Header.Get("Sec-Fetch-Site") {
			case "", "same-origin", "none":
			default:
				WriteError(w, http.StatusForbidden, "cross-site requests are not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

// errNotRunning answers what needs the router while it is down.
var errNotRunning = errors.New("llama-server is not running")

// fail maps an error from any package below to its status.
func (a *api) fail(w http.ResponseWriter, err error) {
	var upstream *llama.ErrUpstream
	var limited *releases.RateLimitError
	var tooBig *http.MaxBytesError
	status := http.StatusInternalServerError
	switch {
	case errors.As(err, &tooBig):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, store.ErrInvalidCursor), errors.Is(err, profiles.ErrInvalid),
		errors.Is(err, builds.ErrInvalidID), errors.Is(err, builds.ErrInvalidBackend), errors.Is(err, releases.ErrInvalidTag):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrNotFound), errors.Is(err, builds.ErrNotFound), errors.Is(err, builds.ErrJobNotFound),
		errors.Is(err, releases.ErrNotFound), errors.Is(err, builds.ErrNoAsset):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrConflict), errors.Is(err, builds.ErrActiveBuild), errors.Is(err, builds.ErrUnsupportedBuild),
		errors.Is(err, supervisor.ErrNoBuild):
		status = http.StatusConflict
	case errors.Is(err, errNotRunning), errors.Is(err, builds.ErrClosed), errors.Is(err, supervisor.ErrStopped):
		status = http.StatusServiceUnavailable
	case errors.As(err, &limited):
		status = http.StatusServiceUnavailable
		if secs := int64(limited.Reset.Sub(a.Now()).Seconds()); secs > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
		}
	case errors.As(err, &upstream):
		status = upstream.Status
		if status < 400 || status >= 500 {
			status = http.StatusBadGateway
		}
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	default:
		a.log.Error("api request failed", "error", err)
	}
	WriteError(w, status, err.Error())
}

func badRequest(w http.ResponseWriter, format string, args ...any) {
	WriteError(w, http.StatusBadRequest, fmt.Sprintf(format, args...))
}

// decodeJSON reads a strict JSON body, answering the failure itself. An empty
// body is allowed when emptyOK, for actions whose fields are all optional.
func (a *api) decodeJSON(w http.ResponseWriter, r *http.Request, into any, emptyOK bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		if errors.Is(err, io.EOF) && emptyOK {
			return true
		}
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			a.fail(w, err)
			return false
		}
		badRequest(w, "invalid JSON body: %v", err)
		return false
	}
	return true
}

// modelIDRe is what a model id may look like: a profile alias, or the
// organisation/repository:tag form of a router model.
var modelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,255}$`)

// query reads validated query parameters, keeping the first failure.
type query struct {
	values url.Values
	err    error
}

func newQuery(r *http.Request) *query { return &query{values: r.URL.Query()} }

func (q *query) str(name string) string { return q.values.Get(name) }

func (q *query) time(name string) time.Time {
	s := q.values.Get(name)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil && q.err == nil {
		q.err = fmt.Errorf("%s: %q is not an RFC 3339 time", name, s)
	}
	return t
}

// integer returns the parameter, or fallback when absent.
func (q *query) integer(name string, fallback, lo, hi int) int {
	s := q.values.Get(name)
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if (err != nil || n < lo || n > hi) && q.err == nil {
		q.err = fmt.Errorf("%s: %q is not a number from %d to %d", name, s, lo, hi)
	}
	return n
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
