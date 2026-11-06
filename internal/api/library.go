package api

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Angelmmiguel/llama-gui/internal/library"
	"github.com/Angelmmiguel/llama-gui/internal/llama"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
)

const (
	hubTimeout = 30 * time.Second
	// catalogTimeout is longer because a cold catalog is three hub calls per entry.
	catalogTimeout = time.Minute
	defaultFitCtx  = 32768
	searchLimit    = 20
	maxSearchQuery = 200
	maxPullFiles   = 128
	maxModelPath   = 1024
)

// kvTypes are the cache types llama-server accepts for -ctk and -ctv.
var kvTypes = []string{"f32", "f16", "bf16", "q8_0", "q5_1", "q5_0", "q4_1", "q4_0", "iq4_nl"}

// listFilters maps the Library's categories to a hub tag. MoE and code have no
// such tag, so a hub search with those lists everything and the caller narrows it.
var listFilters = map[string]string{
	"":          "",
	"chat":      "text-generation",
	"vision":    "image-text-to-text",
	"embedding": "feature-extraction",
	"code":      "",
	"moe":       "",
}

var (
	jobIDRe      = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	retryAfterRe = regexp.MustCompile(`retry after (\d+) s`)
)

// failLibrary maps an error from the library or the hub to its status. Anything
// unrecognised came from the network, and answers with a fixed message so what a
// transport put in its error stays in the log.
func (a *api) failLibrary(w http.ResponseWriter, err error) {
	var pathErr *fs.PathError
	switch {
	case errors.Is(err, context.Canceled):
		return // the client has gone
	case errors.Is(err, context.DeadlineExceeded):
		WriteError(w, http.StatusGatewayTimeout, "Hugging Face took too long to answer")
	case errors.Is(err, library.ErrNotFound), errors.Is(err, library.ErrNoJob):
		WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		a.log.Info("library file not found", "error", err)
		WriteError(w, http.StatusNotFound, "the file was not found")
	case errors.Is(err, library.ErrUnauthorized):
		WriteError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, library.ErrRateLimited):
		if m := retryAfterRe.FindStringSubmatch(err.Error()); m != nil {
			w.Header().Set("Retry-After", m[1])
		}
		WriteError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, library.ErrInvalidRepo), errors.Is(err, library.ErrInvalidName), errors.Is(err, library.ErrNoModel),
		errors.Is(err, library.ErrInvalidPath), errors.Is(err, library.ErrNotModel):
		WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, library.ErrExists), errors.Is(err, library.ErrDuplicate):
		WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, library.ErrNoSpace):
		WriteError(w, http.StatusInsufficientStorage, err.Error())
	case errors.Is(err, library.ErrClosed):
		WriteError(w, http.StatusServiceUnavailable, err.Error())
	case errors.As(err, &pathErr):
		a.log.Error("library file operation failed", "error", err)
		WriteError(w, http.StatusInternalServerError, "a file operation failed; see the server log")
	default:
		a.log.Warn("hugging face request failed", "error", err)
		WriteError(w, http.StatusBadGateway, "could not reach Hugging Face")
	}
}

// fitParams is what a verdict depends on. Context is llama-server's -c, the total
// across the slots.
type fitParams struct {
	ctx, parallel int
	kv            string
	hw            library.Hardware
}

func (p fitParams) estimate(meta library.Metadata, size int64) library.Fit {
	return library.Estimate(meta, size, max(p.ctx/p.parallel, 1), p.kv, p.kv, p.parallel, p.hw)
}

// fitParams reads the fit query parameters; a failure is left in q.err.
func (a *api) fitParams(ctx context.Context, q *query) fitParams {
	p := fitParams{
		ctx:      q.integer("ctx", defaultFitCtx, 256, 1<<24),
		parallel: q.integer("parallel", 1, 1, 256),
		kv:       q.str("kv"),
	}
	if p.kv == "" {
		p.kv = "f16"
	}
	if !slices.Contains(kvTypes, p.kv) && q.err == nil {
		q.err = errors.New("kv: not a cache type: " + strings.Join(kvTypes, ", "))
	}
	if q.err == nil {
		p.hw = a.machine(ctx)
	}
	return p
}

// machine is the memory verdicts are judged against. Only GPUs that report their
// size count, so a machine with none gets the host-only verdicts.
func (a *api) machine(ctx context.Context) library.Hardware {
	snap := a.Hardware(ctx)
	hw := library.Hardware{RAMBytes: int64(snap.Memory.Total), HeadroomBytes: int64(a.MemoryMargin) << 20} //nolint:gosec // memory sizes fit int64
	for _, g := range snap.GPUs {
		if g.MemoryTotal != nil {
			hw.VRAMBytes += int64(*g.MemoryTotal) //nolint:gosec // memory sizes fit int64
		}
	}
	return hw
}

type localProfile struct {
	ID       string `json:"id"`
	Alias    string `json:"alias"`
	CtxSize  int    `json:"ctx_size"`
	Parallel int    `json:"parallel"`
}

type localJSON struct {
	library.LocalModel
	// Profile is what makes the model loadable; RouterID is its name at the router.
	Profile  *localProfile `json:"profile"`
	RouterID string        `json:"router_id,omitempty"`
	Status   string        `json:"status"`
	Fit      library.Fit   `json:"fit"`
}

// routerView is what the rest of the service knows about local models. Either half
// can be missing, because the local list has to keep working without them.
type routerView struct {
	profiles []profiles.Profile
	models   []llama.Model
}

func (a *api) routerView(ctx context.Context) routerView {
	var v routerView
	var err error
	if v.profiles, err = a.Profiles.List(ctx); err != nil {
		a.log.Warn("listing profiles for the library", "error", err)
	}
	router, ctx, cancel, err := a.router(ctx)
	if err != nil {
		return v
	}
	defer cancel()
	if v.models, err = router.Models(ctx); err != nil {
		a.log.Warn("listing router models for the library", "error", err)
	}
	return v
}

func (v routerView) model(id string) (llama.Model, bool) {
	i := slices.IndexFunc(v.models, func(m llama.Model) bool { return m.ID == id })
	if i < 0 {
		return llama.Model{}, false
	}
	return v.models[i], true
}

// link finds how the router knows a local model: through one of its profiles,
// preferring one that is loaded, or by the name the router's own scan gives it.
func (v routerView) link(m library.LocalModel) (prof *profiles.Profile, routerID, status string, measured library.Measured) {
	status = "unloaded"
	for i := range v.profiles {
		p := &v.profiles[i]
		if p.ModelFile != m.Path && !strings.HasPrefix(p.ModelFile, m.Path+"/") {
			continue
		}
		rm, known := v.model(p.Alias)
		if prof == nil || (known && rm.Status == "loaded") {
			prof, routerID = p, p.Alias
			if known {
				status = rm.Status
				measured = measuredOf(rm)
			}
		}
	}
	if prof == nil {
		if rm, known := v.model(m.ID); known {
			routerID, status, measured = rm.ID, rm.Status, measuredOf(rm)
		}
	}
	return prof, routerID, status, measured
}

func measuredOf(m llama.Model) library.Measured {
	var out library.Measured
	if m.Status != "loaded" {
		return out
	}
	for _, d := range m.Memory.Effective.Devices {
		out.ModelBytes += d.Model
		out.ContextBytes += d.Context
		out.ComputeBytes += d.Compute
	}
	return out
}

func (a *api) libraryLocal(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	p := a.fitParams(r.Context(), q)
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	models, err := library.Scan(a.ModelsDir)
	if err != nil {
		a.failLibrary(w, err)
		return
	}
	view := a.routerView(r.Context())
	out := make([]localJSON, 0, len(models))
	for _, m := range models {
		var meta library.Metadata
		if m.Metadata != nil {
			meta = *m.Metadata
		}
		prof, routerID, status, measured := view.link(m)
		row := localJSON{
			LocalModel: m, RouterID: routerID, Status: status,
			Fit: library.Calibrate(p.estimate(meta, m.SizeBytes), measured, p.hw),
		}
		if prof != nil {
			row.Profile = &localProfile{ID: prof.ID, Alias: prof.Alias, CtxSize: prof.CtxSize, Parallel: prof.Parallel}
		}
		out = append(out, row)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"models": out, "hardware": p.hw})
}

func (a *api) libraryDelete(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" || len(rel) > maxModelPath || strings.ContainsFunc(rel, unicode.IsControl) {
		badRequest(w, "path: must name a model relative to the models directory")
		return
	}
	models, err := library.Scan(a.ModelsDir)
	if err != nil {
		a.failLibrary(w, err)
		return
	}
	// A file in a model's directory belongs to that model: compare the first element.
	top, _, _ := strings.Cut(filepath.ToSlash(filepath.Clean(rel)), "/")
	view := a.routerView(r.Context())
	for _, m := range models {
		if m.Path != top {
			continue
		}
		if _, id, status, _ := view.link(m); status == "loaded" || status == "loading" {
			WriteError(w, http.StatusConflict, m.ID+" is in use: unload "+id+" first")
			return
		}
	}
	if err := library.Delete(a.ModelsDir, rel); err != nil {
		a.failLibrary(w, err)
		return
	}
	if a.ReloadModels != nil {
		a.ReloadModels(r.Context())
	}
	WriteJSON(w, http.StatusOK, map[string]string{"path": rel})
}

func (a *api) libraryDisk(w http.ResponseWriter, _ *http.Request) {
	disk, err := library.DiskUsage(a.ModelsDir)
	if err != nil {
		a.failLibrary(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"path": a.ModelsDir, "gguf_bytes": disk.GGUFBytes, "free_bytes": disk.FreeBytes, "total_bytes": disk.TotalBytes,
	})
}

type variantJSON struct {
	library.Variant
	Fit library.Fit `json:"fit"`
	// DownloadFiles is the variant's files plus the mmproj a multimodal model needs.
	DownloadFiles []string `json:"download_files"`
	DownloadBytes int64    `json:"download_bytes"`
}

// repoJSON is a row of the hub list. Variants, Metadata and Best are absent on a
// plain search, as they cost a few hub calls per repo: /library/repo has them.
type repoJSON struct {
	library.SearchResult
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	Catalog       bool              `json:"catalog"`
	Variants      []variantJSON     `json:"variants,omitempty"`
	MMProj        []library.File    `json:"mmproj,omitempty"`
	Metadata      *library.Metadata `json:"metadata,omitempty"`
	MetadataError string            `json:"metadata_error,omitempty"`
	Best          string            `json:"best,omitempty"`
}

func newRepoRow(r library.SearchResult, catalog []library.CatalogEntry) repoJSON {
	row := repoJSON{SearchResult: r, Name: path.Base(r.Repo)}
	if i := slices.IndexFunc(catalog, func(e library.CatalogEntry) bool { return e.Repo == r.Repo }); i >= 0 {
		e := catalog[i]
		row.Name, row.Description, row.Catalog = e.Name, e.Description, true
		row.Tags = slices.Concat(e.Tags, r.Tags)
	}
	if row.Tags == nil {
		row.Tags = []string{}
	}
	return row
}

// mmprojFor picks the projector for a variant: the same quantisation if there is one.
func mmprojFor(v library.Variant, mmproj []library.File) (library.File, bool) {
	if len(mmproj) == 0 {
		return library.File{}, false
	}
	quant := strings.ToLower(v.Quant)
	if i := slices.IndexFunc(mmproj, func(f library.File) bool { return strings.Contains(strings.ToLower(f.Name), quant) }); i >= 0 {
		return mmproj[i], true
	}
	return mmproj[0], true
}

func downloadOf(v library.Variant, mmproj []library.File) ([]string, int64) {
	files := make([]string, 0, len(v.Files)+1)
	size := v.SizeBytes
	for _, f := range v.Files {
		files = append(files, f.Name)
	}
	if f, ok := mmprojFor(v, mmproj); ok {
		files = append(files, f.Name)
		size += f.Size
	}
	return files, size
}

// variantStem is a variant's name without its quantisation. Variants share an
// architecture only within a stem: a repo can also carry another model, such as the
// small draft model next to gpt-oss. Large quantisations often sit in a directory
// of their own, which the base name leaves out.
func variantStem(v library.Variant) string {
	return strings.ReplaceAll(strings.ToLower(path.Base(v.ID)), strings.ToLower(v.Quant), "")
}

// mainStem is the stem of the largest variant, which is the model the repo is about.
func mainStem(variants []library.Variant) string {
	if len(variants) == 0 {
		return ""
	}
	return variantStem(slices.MaxFunc(variants, func(a, b library.Variant) int { return cmp.Compare(a.SizeBytes, b.SizeBytes) }))
}

// bestVariant is the largest of the main model that fits entirely in memory, or
// else its smallest. Variants arrive smallest first.
func bestVariant(variants []variantJSON, main string) string {
	best := ""
	for _, v := range variants {
		if variantStem(v.Variant) != main {
			continue
		}
		if best == "" || v.Fit.Verdict == library.VerdictFitsGPU || v.Fit.Verdict == library.VerdictFitsRAM {
			best = v.ID
		}
	}
	return best
}

// repoRow looks up a repo and reads one header per model in it. A header that can't
// be read only costs the precision of the estimate, as sizes still give a verdict.
func (a *api) repoRow(ctx context.Context, repo string, p fitParams) (repoJSON, error) {
	info, err := a.Hub.Repo(ctx, repo)
	if err != nil {
		return repoJSON{}, err
	}
	row := newRepoRow(info.SearchResult, library.Catalog())
	row.MMProj = info.MMProj
	row.Variants = make([]variantJSON, 0, len(info.Variants))
	main := mainStem(info.Variants)
	metas := map[string]library.Metadata{}
	for _, v := range info.Variants {
		stem := variantStem(v)
		meta, read := metas[stem]
		if !read {
			meta, err = a.Hub.ReadRemoteMetadata(ctx, repo, v.Files[0].Name)
			metas[stem] = meta
			switch {
			case err == nil && stem == main:
				row.Metadata = &meta
			case err != nil && stem == main:
				a.log.Debug("reading remote metadata", "repo", repo, "error", err)
				row.MetadataError = metadataMessage(err)
			}
		}
		out := variantJSON{Variant: v, Fit: p.estimate(meta, v.SizeBytes)}
		out.DownloadFiles, out.DownloadBytes = downloadOf(v, info.MMProj)
		row.Variants = append(row.Variants, out)
	}
	row.Best = bestVariant(row.Variants, main)
	return row, nil
}

// mainCandidate narrows a catalog candidate to the model it is about, with that
// model's header, so a repo's draft model is neither suggested nor judged by.
func (a *api) mainCandidate(ctx context.Context, c library.Candidate) library.Candidate {
	main := mainStem(c.Variants)
	sameHeader := len(c.Variants) == 0 || variantStem(c.Variants[0]) == main
	c.Variants = slices.DeleteFunc(slices.Clone(c.Variants), func(v library.Variant) bool { return variantStem(v) != main })
	if sameHeader {
		return c
	}
	if meta, err := a.Hub.ReadRemoteMetadata(ctx, c.Repo, c.Variants[0].Files[0].Name); err == nil {
		c.Meta = meta
	} else {
		a.log.Debug("reading remote metadata", "repo", c.Repo, "error", err)
		c.Meta = library.Metadata{}
	}
	return c
}

func metadataMessage(err error) string {
	switch {
	case errors.Is(err, library.ErrUnauthorized), errors.Is(err, library.ErrRateLimited), errors.Is(err, library.ErrNotFound):
		return err.Error()
	}
	return "could not read this file's header"
}

func (a *api) libraryRepo(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	repo := q.str("repo")
	p := a.fitParams(r.Context(), q)
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	if !library.ValidRepo(repo) {
		a.failLibrary(w, library.ErrInvalidRepo)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), hubTimeout)
	defer cancel()
	row, err := a.repoRow(ctx, repo, p)
	if err != nil {
		a.failLibrary(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"repo": row, "hardware": p.hw})
}

func (a *api) librarySearch(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q.str("q")), "https://huggingface.co/"))
	filter := q.str("filter")
	p := a.fitParams(r.Context(), q)
	switch hubTag, known := listFilters[filter]; {
	case q.err != nil:
		badRequest(w, "%v", q.err)
	case !known:
		badRequest(w, "filter: %q is not chat, vision, embedding, code or moe", filter)
	case len(text) > maxSearchQuery || strings.ContainsFunc(text, unicode.IsControl):
		badRequest(w, "q: too long, or holds control characters")
	case text == "":
		a.searchCatalog(w, r, filter, p)
	case library.ValidRepo(text):
		ctx, cancel := context.WithTimeout(r.Context(), hubTimeout)
		defer cancel()
		row, err := a.repoRow(ctx, text, p)
		if err != nil {
			a.failLibrary(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"source": "repo", "results": []repoJSON{row}, "hardware": p.hw})
	default:
		ctx, cancel := context.WithTimeout(r.Context(), hubTimeout)
		defer cancel()
		results, err := a.Hub.Search(ctx, text, hubTag, searchLimit)
		if err != nil {
			a.failLibrary(w, err)
			return
		}
		catalog := library.Catalog()
		rows := make([]repoJSON, 0, len(results))
		for _, res := range results {
			rows = append(rows, newRepoRow(res, catalog))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"source": "search", "results": rows, "hardware": p.hw})
	}
}

func (a *api) searchCatalog(w http.ResponseWriter, r *http.Request, filter string, p fitParams) {
	ctx, cancel := context.WithTimeout(r.Context(), catalogTimeout)
	defer cancel()
	cands, err := a.Hub.Candidates(ctx)
	if err != nil {
		a.failLibrary(w, err)
		return
	}
	rows := make([]repoJSON, 0, len(cands))
	for _, c := range cands {
		if filter != "" && !slices.Contains(c.Tags, filter) {
			continue
		}
		// The hub client has what resolving the catalog just read cached.
		row, err := a.repoRow(ctx, c.Repo, p)
		if err != nil {
			continue
		}
		rows = append(rows, row)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"source": "catalog", "results": rows, "hardware": p.hw})
}

type suggestionJSON struct {
	library.Suggestion
	Metadata      library.Metadata `json:"metadata"`
	DownloadFiles []string         `json:"download_files"`
	DownloadBytes int64            `json:"download_bytes"`
}

func (a *api) librarySuggestions(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	p := a.fitParams(r.Context(), q)
	if q.err != nil {
		badRequest(w, "%v", q.err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), catalogTimeout)
	defer cancel()
	cands, err := a.Hub.Candidates(ctx)
	if err != nil {
		a.failLibrary(w, err)
		return
	}
	for i, c := range cands {
		cands[i] = a.mainCandidate(ctx, c)
	}
	out := []suggestionJSON{}
	for _, s := range library.Suggest(cands, p.hw, p.ctx, p.kv) {
		row := suggestionJSON{Suggestion: s}
		if i := slices.IndexFunc(cands, func(c library.Candidate) bool { return c.Repo == s.Repo }); i >= 0 {
			row.Metadata = cands[i].Meta
		}
		var mmproj []library.File
		if info, err := a.Hub.Repo(ctx, s.Repo); err == nil {
			mmproj = info.MMProj
		}
		row.DownloadFiles, row.DownloadBytes = downloadOf(s.Variant, mmproj)
		out = append(out, row)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"suggestions": out, "hardware": p.hw})
}

func (a *api) listDownloads(w http.ResponseWriter, _ *http.Request) {
	jobs := a.Downloads.List()
	if jobs == nil {
		jobs = []library.Job{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (a *api) createDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo  string   `json:"repo"`
		Files []string `json:"files"`
	}
	if !a.decodeJSON(w, r, &body, false) {
		return
	}
	if !library.ValidRepo(body.Repo) {
		a.failLibrary(w, library.ErrInvalidRepo)
		return
	}
	if len(body.Files) == 0 || len(body.Files) > maxPullFiles {
		badRequest(w, "files: name from 1 to %s files of the repository", strconv.Itoa(maxPullFiles))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), hubTimeout)
	defer cancel()
	job, err := a.Downloads.Enqueue(ctx, body.Repo, body.Files...)
	if err != nil {
		a.failLibrary(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, job)
}

// cancelDownload keeps what was downloaded so the same download can resume,
// unless told otherwise with ?remove_partials=true.
func (a *api) cancelDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !jobIDRe.MatchString(id) {
		badRequest(w, "invalid download id")
		return
	}
	remove := false
	if s := r.URL.Query().Get("remove_partials"); s != "" {
		var err error
		if remove, err = strconv.ParseBool(s); err != nil {
			badRequest(w, "remove_partials: %q is not a boolean", s)
			return
		}
	}
	if err := a.Downloads.Cancel(id, remove); err != nil {
		a.failLibrary(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"id": id})
}
