package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/Angelmmiguel/llama-gui/internal/library"
	"github.com/Angelmmiguel/llama-gui/internal/profiles"
)

// manifestEntry is one profile for profiles sync. Model, a Hugging Face reference
// such as hf.co/user/repo:Q4_K_M, stands for the profile's model_file and mmproj:
// sync downloads that quantisation when it is missing and fills them in.
type manifestEntry struct {
	Model   string           `json:"model,omitempty"`
	Profile profiles.Profile `json:"profile"`
}

// hfRef is a parsed manifestEntry.Model.
type hfRef struct{ repo, quant string }

func parseHFRef(s string) (hfRef, error) {
	ref := s
	for _, prefix := range []string{"https://", "http://", "hf.co/", "huggingface.co/"} {
		ref = strings.TrimPrefix(ref, prefix)
	}
	repo, quant, ok := strings.Cut(ref, ":")
	if !ok || quant == "" || !library.ValidRepo(repo) {
		return hfRef{}, fmt.Errorf("model %q: want [hf.co/]user/repo:QUANT", s)
	}
	return hfRef{repo, quant}, nil
}

func profilesCommand(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageError(stderr, "profiles needs a subcommand")
	}
	sub, args := args[0], args[1:]
	var prefix string
	c, rest, code := clientCommand("profiles "+sub, args, getenv, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&prefix, "prefix", "", "id prefix of the profiles the manifest owns")
	})
	if code != 0 {
		return code
	}
	switch sub {
	case "sync":
		if len(rest) != 1 {
			return usageError(stderr, "profiles sync needs a manifest file")
		}
		if prefix == "" {
			return usageError(stderr, "profiles sync needs --prefix")
		}
		entries, err := readManifest(rest[0], prefix)
		if err != nil {
			return finish(stderr, err)
		}
		return finish(stderr, c.syncProfiles(context.Background(), entries, prefix, stdout))
	}
	return usageError(stderr, "unknown profiles subcommand %q", sub)
}

func readManifest(path, prefix string) ([]manifestEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var entries []manifestEntry
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		id := e.Profile.ID
		if !strings.HasPrefix(id, prefix) || id == prefix {
			return nil, fmt.Errorf("%s: profile id %q does not start with %q", path, id, prefix)
		}
		if seen[id] {
			return nil, fmt.Errorf("%s: profile id %q appears twice", path, id)
		}
		seen[id] = true
		if (e.Model == "") == (e.Profile.ModelFile == "") {
			return nil, fmt.Errorf("%s: %s: give either model or profile.model_file", path, id)
		}
		if e.Model != "" {
			if _, err := parseHFRef(e.Model); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", path, id, err)
			}
		}
	}
	return entries, nil
}

// syncProfiles makes the service's profiles whose ids start with prefix those of
// the manifest: it downloads missing models, creates or replaces what differs,
// reloads a changed profile that is loaded, loads a load_at_start profile that is
// not, and deletes the rest. Every other profile is left alone, and a second run
// with the same manifest changes nothing.
func (c *apiClient) syncProfiles(ctx context.Context, entries []manifestEntry, prefix string, w io.Writer) error {
	declared := map[string]bool{}
	for _, e := range entries {
		p := e.Profile
		declared[p.ID] = true

		path := "/profiles/" + url.PathEscape(p.ID)
		var current profiles.Profile
		err := c.do(ctx, http.MethodGet, path, nil, &current)
		exists := err == nil
		if err != nil && !isStatus(err, http.StatusNotFound) {
			return fmt.Errorf("%s: %w", p.ID, err)
		}
		if e.Model != "" {
			var keep *profiles.Profile
			if exists {
				keep = &current
			}
			if err := c.resolveModel(ctx, e.Model, &p, keep, w); err != nil {
				return fmt.Errorf("%s: %w", p.ID, err)
			}
		}
		local, err := c.localModels(ctx)
		if err != nil {
			return err
		}

		// The router honours load-on-startup only when it starts, before this runs:
		// a profile created or changed since, or whose model was still downloading,
		// would stay unloaded until llama-gui restarts.
		load := p.LoadAtStart && localFor(local, p.ModelFile).Status == "unloaded"
		switch {
		case !exists:
			if err := c.do(ctx, http.MethodPost, "/profiles", p, nil); err != nil {
				return fmt.Errorf("%s: %w", p.ID, err)
			}
			_, _ = fmt.Fprintf(w, "%s: created\n", p.ID)
		case sameProfile(current, p):
			_, _ = fmt.Fprintf(w, "%s: up to date\n", p.ID)
		default:
			if err := c.do(ctx, http.MethodPut, path, p, nil); err != nil {
				return fmt.Errorf("%s: %w", p.ID, err)
			}
			_, _ = fmt.Fprintf(w, "%s: updated\n", p.ID)
			// The router rereads the preset, but a loaded model keeps the arguments it
			// started with. The file can be the same while the alias is not.
			reloaded, err := c.reload(ctx, localFor(local, current.ModelFile), current.Alias, p.Alias, w)
			if err != nil {
				return fmt.Errorf("%s: %w", p.ID, err)
			}
			load = load && !reloaded
		}
		if load {
			if err := c.do(ctx, http.MethodPost, "/models/"+url.PathEscape(p.Alias)+"/load", nil, nil); err != nil {
				return fmt.Errorf("%s: %w", p.ID, err)
			}
			_, _ = fmt.Fprintf(w, "%s: loaded\n", p.Alias)
		}
	}

	var list struct {
		Profiles []profiles.Profile `json:"profiles"`
	}
	if err := c.do(ctx, http.MethodGet, "/profiles", nil, &list); err != nil {
		return err
	}
	for _, p := range list.Profiles {
		if !strings.HasPrefix(p.ID, prefix) || declared[p.ID] {
			continue
		}
		if err := c.do(ctx, http.MethodDelete, "/profiles/"+url.PathEscape(p.ID), nil, nil); err != nil {
			return fmt.Errorf("%s: %w", p.ID, err)
		}
		_, _ = fmt.Fprintf(w, "%s: deleted\n", p.ID)
	}
	return nil
}

// sameProfile compares profiles as the service stores them, so that an empty
// list and no list, or a zero and an omitted field, are the same.
func sameProfile(a, b profiles.Profile) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// resolveModel fills p's model_file, and its mmproj unless p sets one, from ref:
// the local model holding that quantisation, downloaded first when there is none.
// When Hugging Face cannot be asked, keep's files stand if they are still on disk,
// so that a machine without network keeps its profiles as they are.
func (c *apiClient) resolveModel(ctx context.Context, ref string, p *profiles.Profile, keep *profiles.Profile, w io.Writer) error {
	r, err := parseHFRef(ref)
	if err != nil {
		return err
	}
	v, err := c.variant(ctx, r)
	if err != nil {
		if keep == nil || isStatus(err, http.StatusNotFound) {
			return err
		}
		local, lerr := c.localModels(ctx)
		if lerr != nil || localFor(local, keep.ModelFile).Path == "" {
			return err
		}
		_, _ = fmt.Fprintf(w, "%s: keeping %s, as %s cannot be looked up: %v\n", p.ID, keep.ModelFile, ref, err)
		p.ModelFile = keep.ModelFile
		if p.MMProj == "" {
			p.MMProj = keep.MMProj
		}
		return nil
	}
	if v.LocalPath == "" {
		if err := c.pull(ctx, r.repo, v.DownloadFiles, w); err != nil {
			return err
		}
		if v, err = c.variant(ctx, r); err != nil {
			return err
		}
		if v.LocalPath == "" {
			return fmt.Errorf("%s: downloaded, but the service does not find it", ref)
		}
	}
	local, err := c.localModels(ctx)
	if err != nil {
		return err
	}
	m := local[v.LocalPath]
	p.ModelFile = m.mainFile()
	if p.MMProj == "" && m.MMProj != "" {
		p.MMProj = m.Path + "/" + m.MMProj
	}
	return nil
}

// repoVariant is a quantisation of a repository as the Library lists it.
type repoVariant struct {
	Quant         string   `json:"quant"`
	Companion     bool     `json:"companion"`
	DownloadFiles []string `json:"download_files"`
	LocalPath     string   `json:"local_path"`
}

// variant finds r's quantisation among its repository's models, not the draft
// models or MTP heads that can sit next to them.
func (c *apiClient) variant(ctx context.Context, r hfRef) (repoVariant, error) {
	var out struct {
		Repo struct {
			Variants []repoVariant `json:"variants"`
		} `json:"repo"`
	}
	if err := c.do(ctx, http.MethodGet, "/library/repo?repo="+url.QueryEscape(r.repo), nil, &out); err != nil {
		return repoVariant{}, err
	}
	var found []repoVariant
	var quants []string
	for _, v := range out.Repo.Variants {
		if v.Companion {
			continue
		}
		quants = append(quants, v.Quant)
		if strings.EqualFold(v.Quant, r.quant) {
			found = append(found, v)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return repoVariant{}, &statusError{code: http.StatusNotFound,
			msg: fmt.Sprintf("%s has no %s, only %s", r.repo, r.quant, strings.Join(quants, ", "))}
	}
	return repoVariant{}, fmt.Errorf("%s has %d models in %s", r.repo, len(found), r.quant)
}

// localModel is a model on disk, a GGUF or a directory of them, as the service
// links it to the router.
type localModel struct {
	Path     string   `json:"path"`
	Files    []string `json:"files"`
	MMProj   string   `json:"mmproj"`
	RouterID string   `json:"router_id"`
	Status   string   `json:"status"`
}

// mainFile is the file a profile loads, the way the Profiles page picks it: a
// directory's first shard or its one model.
func (m localModel) mainFile() string {
	if len(m.Files) == 1 && m.Files[0] == m.Path {
		return m.Path
	}
	var names []string
	for _, f := range m.Files {
		if f != m.MMProj && !strings.Contains(strings.ToLower(f), "mmproj") {
			names = append(names, f)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return m.Path
	}
	main := names[0]
	for _, f := range names {
		if firstShardRe.MatchString(f) {
			main = f
			break
		}
	}
	return m.Path + "/" + main
}

var firstShardRe = regexp.MustCompile(`(?i)-0*1-of-\d+\.gguf$`)

func (c *apiClient) localModels(ctx context.Context) (map[string]localModel, error) {
	var out struct {
		Models []localModel `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, "/library/local", nil, &out); err != nil {
		return nil, err
	}
	m := make(map[string]localModel, len(out.Models))
	for _, l := range out.Models {
		m[l.Path] = l
	}
	return m, nil
}

// localFor is the local model a profile's model_file is in, zero if none.
func localFor(local map[string]localModel, modelFile string) localModel {
	if m, ok := local[modelFile]; ok {
		return m
	}
	if dir, _, ok := strings.Cut(modelFile, "/"); ok {
		return local[dir]
	}
	return localModel{}
}

// reload restarts a loaded model on its new profile, under its new alias, and
// reports whether it did.
func (c *apiClient) reload(ctx context.Context, m localModel, oldAlias, newAlias string, w io.Writer) (bool, error) {
	if m.Status != "loaded" && m.Status != "loading" {
		return false, nil
	}
	id := m.RouterID
	if id == "" {
		id = oldAlias
	}
	if err := c.do(ctx, http.MethodPost, "/models/"+url.PathEscape(id)+"/unload", nil, nil); err != nil {
		return false, err
	}
	if err := c.do(ctx, http.MethodPost, "/models/"+url.PathEscape(newAlias)+"/load", nil, nil); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(w, "%s: reloaded\n", newAlias)
	return true, nil
}

// statusError is a non-2xx answer from the service.
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

func isStatus(err error, code int) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == code
}
