package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultHFBaseURL is the Hugging Face hub.
const DefaultHFBaseURL = "https://huggingface.co"

// Errors from the Hugging Face client.
var (
	ErrInvalidRepo  = errors.New("invalid repository name, expected user/repo")
	ErrInvalidName  = errors.New("invalid file name")
	ErrNotFound     = errors.New("not found on Hugging Face")
	ErrUnauthorized = errors.New("hugging face refused access: the repository is gated or private, so a Hugging Face token may be required")
	ErrRateLimited  = errors.New("hugging face rate limit reached: wait a few minutes, or add a Hugging Face token for a higher limit")
)

const (
	remoteMetaStart = 256 << 10
	remoteMetaLimit = 64 << 20
	// remoteMTPBytes reaches past the model keys, which took under 4 KiB in every real file checked.
	remoteMTPBytes = 256 << 10
	maxTreePages   = 8
	searchTTL      = time.Hour
	repoTTL        = time.Hour
	metaTTL        = time.Hour
	cacheMax       = 512
)

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	filterRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`)
	// nameRe allows the characters real GGUF names use and nothing that could act as a path.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+()=@ -]{0,254}$`)
)

// ValidRepo reports whether s is a well-formed "user/repo" name.
func ValidRepo(s string) bool { return repoRe.MatchString(s) && !strings.Contains(s, "..") }

// validRepoFile validates a file path inside a repo (optionally in subdirectories) that ends in .gguf,
// returning its final element, which is the only part ever used locally.
func validRepoFile(p string) (string, error) {
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if !nameRe.MatchString(s) || strings.Contains(s, "..") {
			return "", fmt.Errorf("%w: %q", ErrInvalidName, p)
		}
	}
	base := segs[len(segs)-1]
	if !isGGUF(base) {
		return "", fmt.Errorf("%w: %q is not a .gguf file", ErrInvalidName, p)
	}
	return base, nil
}

// Client talks to the Hugging Face hub.
type Client struct {
	hc    *http.Client
	base  string
	token string
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	v   any
	exp time.Time
}

// NewClient returns a hub client. A nil hc gets one that follows at most five redirects.
func NewClient(hc *http.Client, baseURL, token string) *Client {
	if hc == nil {
		hc = &http.Client{CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		}}
	}
	if baseURL == "" {
		baseURL = DefaultHFBaseURL
	}
	return &Client{hc: hc, base: strings.TrimRight(baseURL, "/"), token: token, now: time.Now, cache: map[string]cacheEntry{}}
}

// HasToken reports whether a Hugging Face token is configured.
func (c *Client) HasToken() bool { return c.token != "" }

func (c *Client) cached(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok || !c.now().Before(e.exp) {
		delete(c.cache, key)
		return nil, false
	}
	return e.v, true
}

func (c *Client) store(key string, v any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= cacheMax {
		now := c.now()
		for k, e := range c.cache {
			if !now.Before(e.exp) {
				delete(c.cache, k)
			}
		}
		for k := range c.cache {
			if len(c.cache) < cacheMax {
				break
			}
			delete(c.cache, k)
		}
	}
	c.cache[key] = cacheEntry{v, c.now().Add(ttl)}
}

func (c *Client) request(ctx context.Context, u string, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("User-Agent", "llama-gui")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if err := statusError(resp); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

func statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests:
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if _, err := strconv.Atoi(ra); err == nil {
				return fmt.Errorf("%w (retry after %s s)", ErrRateLimited, ra)
			}
		}
		return ErrRateLimited
	case resp.StatusCode >= 300:
		return fmt.Errorf("hub returned %s", resp.Status)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, u string, v any) (http.Header, error) {
	resp, err := c.request(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v); err != nil {
		return nil, fmt.Errorf("decode Hugging Face response: %w", err)
	}
	return resp.Header, nil
}

// SearchResult is one repository in a search listing.
type SearchResult struct {
	Repo         string   `json:"repo"`
	Author       string   `json:"author"`
	Downloads    int64    `json:"downloads"`
	Likes        int64    `json:"likes"`
	LastModified string   `json:"last_modified"`
	Gated        bool     `json:"gated"`
	Tags         []string `json:"tags"`
}

// hfFlag decodes the hub's gated field, which is false or a string such as "auto".
type hfFlag bool

func (f *hfFlag) UnmarshalJSON(b []byte) error {
	*f = hfFlag(string(b) != "false" && string(b) != "null")
	return nil
}

type hfModel struct {
	ID           string   `json:"id"`
	Author       string   `json:"author"`
	Downloads    int64    `json:"downloads"`
	Likes        int64    `json:"likes"`
	LastModified string   `json:"lastModified"`
	Gated        hfFlag   `json:"gated"`
	Tags         []string `json:"tags"`
}

func (m hfModel) result() SearchResult {
	return SearchResult{Repo: m.ID, Author: m.Author, Downloads: m.Downloads, Likes: m.Likes,
		LastModified: m.LastModified, Gated: bool(m.Gated), Tags: m.Tags}
}

// SearchPage is one page of a search listing. Next is empty on the last page.
type SearchPage struct {
	Results []SearchResult
	Next    string
}

// Search lists GGUF repositories matching query, most downloaded first. filter optionally adds a hub
// tag such as "image-text-to-text", and cursor continues from an earlier page's Next.
func (c *Client) Search(ctx context.Context, query, filter, cursor string, limit int) (SearchPage, error) {
	if filter != "" && !filterRe.MatchString(filter) {
		return SearchPage{}, fmt.Errorf("invalid filter %q", filter)
	}
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 50)
	q := url.Values{"filter": {"gguf"}, "sort": {"downloads"}, "full": {"false"}, "limit": {strconv.Itoa(limit)}}
	if filter != "" {
		q.Add("filter", filter)
	}
	if query = strings.TrimSpace(query); query != "" {
		q.Set("search", query)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	u := c.base + "/api/models?" + q.Encode()
	if v, ok := c.cached(u); ok {
		return v.(SearchPage), nil
	}
	var raw []hfModel
	header, err := c.getJSON(ctx, u, &raw)
	if err != nil {
		return SearchPage{}, err
	}
	out := SearchPage{Results: make([]SearchResult, 0, len(raw)), Next: nextCursor(header.Get("Link"))}
	for _, m := range raw {
		if ValidRepo(m.ID) {
			out.Results = append(out.Results, m.result())
		}
	}
	c.store(u, out, searchTTL)
	return out, nil
}

var nextLinkRe = regexp.MustCompile(`<([^>]*)>\s*;\s*rel="next"`)

// nextCursor takes only the cursor from a Link header, as the rest of its URL is the hub's to say
// and every request must keep going to the configured host.
func nextCursor(link string) string {
	m := nextLinkRe.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	u, err := url.Parse(m[1])
	if err != nil {
		return ""
	}
	return u.Query().Get("cursor")
}

// File is one GGUF file in a repository.
type File struct {
	Name   string `json:"name"` // path inside the repo
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Variant is one quantisation of a model: a single file, or all shards of it.
type Variant struct {
	ID        string `json:"id"`
	Quant     string `json:"quant"`
	Files     []File `json:"files"`
	SizeBytes int64  `json:"size_bytes"`
	Shards    int    `json:"shards"`
}

// RepoInfo is a repository with its GGUF files grouped for the Library.
type RepoInfo struct {
	SearchResult
	Variants []Variant `json:"variants"`
	MMProj   []File    `json:"mmproj"`
}

// Repo looks up repo ("user/name") and groups its GGUF files into variants by quantisation.
func (c *Client) Repo(ctx context.Context, repo string) (RepoInfo, error) {
	if !ValidRepo(repo) {
		return RepoInfo{}, ErrInvalidRepo
	}
	key := "repo:" + repo
	if v, ok := c.cached(key); ok {
		return v.(RepoInfo), nil
	}
	var m hfModel
	if _, err := c.getJSON(ctx, c.base+"/api/models/"+repo, &m); err != nil {
		return RepoInfo{}, err
	}
	files, err := c.tree(ctx, repo)
	if err != nil {
		return RepoInfo{}, err
	}
	info := RepoInfo{SearchResult: m.result()}
	info.Repo = repo
	info.Variants, info.MMProj = groupFiles(files)
	c.store(key, info, repoTTL)
	return info, nil
}

type hfTreeEntry struct {
	Type string `json:"type"`
	Size int64  `json:"size"`
	Path string `json:"path"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

// tree lists the GGUF files of a repo, following the hub's Link-header pagination.
func (c *Client) tree(ctx context.Context, repo string) ([]File, error) {
	u := c.base + "/api/models/" + repo + "/tree/main?recursive=true"
	var files []File
	for range maxTreePages {
		var page []hfTreeEntry
		hdr, err := c.getJSON(ctx, u, &page)
		if err != nil {
			return nil, err
		}
		for _, e := range page {
			if e.Type != "file" || !isGGUF(e.Path) {
				continue
			}
			if _, err := validRepoFile(e.Path); err != nil {
				continue // never surface a name we would refuse to download
			}
			f := File{Name: e.Path, Size: e.Size}
			if e.LFS != nil {
				f.SHA256, f.Size = e.LFS.OID, e.LFS.Size
			}
			files = append(files, f)
		}
		m := nextLinkRe.FindStringSubmatch(hdr.Get("Link"))
		if m == nil {
			break
		}
		next, err := url.Parse(m[1])
		base, _ := url.Parse(c.base)
		if err != nil || next.Host != base.Host {
			break
		}
		u = next.String()
	}
	return files, nil
}

// groupFiles groups files by model stem (shards combined), separates the mmproj files and
// leaves out imatrix files, which can't be loaded.
func groupFiles(files []File) (variants []Variant, mmproj []File) {
	byID := map[string]*Variant{}
	for _, f := range files {
		base := path.Base(f.Name)
		if isMMProjName(base) {
			mmproj = append(mmproj, f)
			continue
		}
		if isIMatrixName(base) {
			continue
		}
		id := strings.TrimSuffix(f.Name, ".gguf")
		if loc := shardRe.FindStringIndex(f.Name); loc != nil {
			id = f.Name[:loc[0]]
		}
		v := byID[id]
		if v == nil {
			v = &Variant{ID: id, Quant: quantFromName(base)}
			if v.Quant == "" {
				v.Quant = "unknown"
			}
			byID[id] = v
		}
		v.Files = append(v.Files, f)
		v.SizeBytes += f.Size
		v.Shards = len(v.Files)
	}
	for _, v := range byID {
		slices.SortFunc(v.Files, func(a, b File) int { return strings.Compare(a.Name, b.Name) })
		variants = append(variants, *v)
	}
	slices.SortFunc(variants, func(a, b Variant) int {
		if a.SizeBytes != b.SizeBytes {
			return int(max(-1, min(1, a.SizeBytes-b.SizeBytes)))
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(mmproj, func(a, b File) int { return strings.Compare(a.Name, b.Name) })
	return variants, mmproj
}

// resolveURL is the download URL of a file in repo's main branch.
func (c *Client) resolveURL(repo, file string) string {
	segs := strings.Split(file, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.base + "/" + repo + "/resolve/main/" + strings.Join(segs, "/")
}

// ReadRemoteMetadata parses a repo file's GGUF header with Range requests, fetching a growing prefix
// (tokenizer vocabularies can make headers many MiB) until it parses or the limit is reached.
func (c *Client) ReadRemoteMetadata(ctx context.Context, repo, file string) (Metadata, error) {
	if !ValidRepo(repo) {
		return Metadata{}, ErrInvalidRepo
	}
	if _, err := validRepoFile(file); err != nil {
		return Metadata{}, err
	}
	key := "meta:" + repo + "/" + file
	if v, ok := c.cached(key); ok {
		return v.(Metadata), nil
	}
	u := c.resolveURL(repo, file)
	for n := int64(remoteMetaStart); ; n *= 4 {
		n = min(n, remoteMetaLimit)
		buf, err := c.prefix(ctx, u, n)
		if err != nil {
			return Metadata{}, err
		}
		m, err := ParseGGUF(bytes.NewReader(buf), int64(len(buf)))
		switch {
		case err == nil:
			c.store(key, m, metaTTL)
			return m, nil
		case !errors.Is(err, ErrTruncated) && !errors.Is(err, ErrTooLarge):
			return Metadata{}, err
		case int64(len(buf)) < n:
			return Metadata{}, ErrTruncated // the whole file was shorter than its header claims
		case n >= remoteMetaLimit:
			return Metadata{}, ErrTooLarge
		}
	}
}

// ReadRemoteMTP says how a repo file offers Multi Token Prediction, reading far less of it than
// ReadRemoteMetadata does.
func (c *Client) ReadRemoteMTP(ctx context.Context, repo, file string) (MTP, error) {
	if !ValidRepo(repo) {
		return MTPNone, ErrInvalidRepo
	}
	if _, err := validRepoFile(file); err != nil {
		return MTPNone, err
	}
	key := "mtp:" + repo + "/" + file
	if v, ok := c.cached(key); ok {
		return v.(MTP), nil
	}
	buf, err := c.prefix(ctx, c.resolveURL(repo, file), remoteMTPBytes)
	if err != nil {
		return MTPNone, err
	}
	m, err := ScanMTP(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return MTPNone, err
	}
	c.store(key, m, metaTTL)
	return m, nil
}

func (c *Client) prefix(ctx context.Context, u string, n int64) ([]byte, error) {
	resp, err := c.request(ctx, u, http.Header{"Range": {fmt.Sprintf("bytes=0-%d", n-1)}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, n)) // a server that ignores Range sends 200: stop at n
}
