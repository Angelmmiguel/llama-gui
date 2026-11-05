// Package releases reads the llama.cpp releases from the GitHub API.
package releases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is the public GitHub API.
	DefaultBaseURL = "https://api.github.com"

	repoPath      = "/repos/ggml-org/llama.cpp"
	nightlyAsset  = "nightly-tag.txt"
	cacheTTL      = 10 * time.Minute
	maxCached     = 64
	maxPages      = 10
	perPage       = 100
	defaultLimit  = 30
	maxBodyBytes  = 64 << 20
	maxTagFile    = 64
	limitFallback = time.Minute
)

// ErrNotFound means the release or asset does not exist.
var ErrNotFound = errors.New("release not found")

// ErrRateLimited is matched by a RateLimitError.
var ErrRateLimited = errors.New("github rate limit exceeded")

// RateLimitError says when the rate limit lifts. It matches ErrRateLimited.
type RateLimitError struct {
	Reset time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%v until %s", ErrRateLimited, e.Reset.UTC().Format(time.RFC3339))
}

// Is implements errors.Is.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// Release is a GitHub release. Slices are shared with the cache, so treat them as read-only.
type Release struct {
	Tag string `json:"tag"`
	// BinaryTag is the b* tag holding the binaries. List leaves it empty on stable releases,
	// since finding it takes another download; ResolveVersion always sets it.
	BinaryTag   string    `json:"binary_tag"`
	Channel     Channel   `json:"channel"`
	PublishedAt time.Time `json:"published_at"`
	Body        string    `json:"body"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a file attached to a release. Digest is GitHub's "sha256:<hex>", empty on old assets.
type Asset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
	Digest string `json:"digest"`
}

// Comparison is how far head is from base. Commits may be fewer than AheadBy, as GitHub truncates.
type Comparison struct {
	AheadBy int      `json:"ahead_by"`
	Commits []Commit `json:"commits"`
}

// Commit is one commit of a Comparison, oldest first.
type Commit struct {
	SHA   string `json:"sha"`
	Title string `json:"title"`
}

// Client talks to the GitHub API, caching responses and revalidating them with ETags.
type Client struct {
	http  *http.Client
	base  string
	token string
	now   func() time.Time

	mu           sync.Mutex
	cache        map[string]*cacheEntry
	limitedUntil time.Time
}

type cacheEntry struct {
	value   any
	etag    string
	next    string
	fetched time.Time
}

// NewClient returns a client. An empty baseURL means DefaultBaseURL, and an empty token means
// unauthenticated requests.
func NewClient(httpClient *http.Client, baseURL, token string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		http:  httpClient,
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		now:   time.Now,
		cache: map[string]*cacheEntry{},
	}
}

type apiRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	PublishedAt time.Time `json:"published_at"`
	Body        string    `json:"body"`
	Assets      []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type apiComparison struct {
	AheadBy int `json:"ahead_by"`
	Commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	} `json:"commits"`
}

func (r apiRelease) release() Release {
	out := Release{Tag: r.TagName, Channel: ClassifyTag(r.TagName), PublishedAt: r.PublishedAt, Body: r.Body}
	if out.Channel == ChannelNightly {
		out.BinaryTag = r.TagName
	}
	out.Assets = make([]Asset, len(r.Assets))
	for i, a := range r.Assets {
		out.Assets[i] = Asset{Name: a.Name, Size: a.Size, URL: a.BrowserDownloadURL, Digest: a.Digest}
	}
	return out
}

// List returns up to limit releases of a channel, newest first. Stable releases sit between
// dozens of b* tags per day, so it walks a bounded number of pages and, once it has something,
// treats a failure or running out of pages as the end of the list.
func (c *Client) List(ctx context.Context, channel Channel, limit int) ([]Release, error) {
	if channel != ChannelStable && channel != ChannelNightly {
		return nil, fmt.Errorf("unknown channel %q", channel)
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	var out []Release
	next := fmt.Sprintf("%s%s/releases?per_page=%d", c.base, repoPath, perPage)
	for page := 0; page < maxPages && next != "" && len(out) < limit; page++ {
		items, link, err := fetchJSON[[]apiRelease](ctx, c, next)
		if err != nil {
			if len(out) > 0 && ctx.Err() == nil {
				break
			}
			return nil, err
		}
		for _, r := range items {
			if !r.Draft && ClassifyTag(r.TagName) == channel {
				out = append(out, r.release())
			}
		}
		next = c.sameOrigin(link)
	}
	slices.SortStableFunc(out, func(a, b Release) int {
		switch {
		case newer(a, b):
			return -1
		case newer(b, a):
			return 1
		}
		return 0
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ResolveVersion turns "stable", "nightly" or a tag into a release ready to install: BinaryTag
// is set and Assets are the binaries, which for a stable release are those of its b* tag.
func (c *Client) ResolveVersion(ctx context.Context, version string) (Release, error) {
	switch version {
	case string(ChannelStable):
		rel, err := c.newestStable(ctx)
		if err != nil {
			return Release{}, err
		}
		return c.withBinaries(ctx, rel)
	case string(ChannelNightly):
		list, err := c.List(ctx, ChannelNightly, 1)
		if err != nil {
			return Release{}, err
		}
		if len(list) == 0 {
			return Release{}, fmt.Errorf("%w: no nightly release", ErrNotFound)
		}
		return list[0], nil
	}
	rel, err := c.byTag(ctx, version)
	if err != nil {
		return Release{}, err
	}
	return c.withBinaries(ctx, rel)
}

func (c *Client) newestStable(ctx context.Context) (Release, error) {
	latest, _, err := fetchJSON[apiRelease](ctx, c, c.base+repoPath+"/releases/latest")
	if err == nil && ClassifyTag(latest.TagName) == ChannelStable {
		return latest.release(), nil
	}
	if err != nil && (errors.Is(err, ErrRateLimited) || ctx.Err() != nil) {
		return Release{}, err
	}
	list, err := c.List(ctx, ChannelStable, 1)
	if err != nil {
		return Release{}, err
	}
	if len(list) == 0 {
		return Release{}, fmt.Errorf("%w: no stable release", ErrNotFound)
	}
	return list[0], nil
}

func (c *Client) byTag(ctx context.Context, tag string) (Release, error) {
	if err := ValidateTag(tag); err != nil {
		return Release{}, err
	}
	r, _, err := fetchJSON[apiRelease](ctx, c, c.base+repoPath+"/releases/tags/"+url.PathEscape(tag))
	if err != nil {
		return Release{}, fmt.Errorf("release %s: %w", tag, err)
	}
	return r.release(), nil
}

func (c *Client) withBinaries(ctx context.Context, rel Release) (Release, error) {
	if rel.Channel != ChannelStable {
		return rel, nil
	}
	i := slices.IndexFunc(rel.Assets, func(a Asset) bool { return a.Name == nightlyAsset })
	if i < 0 {
		return Release{}, fmt.Errorf("%w: %s has no %s", ErrNotFound, rel.Tag, nightlyAsset)
	}
	tag, err := c.fetchBinaryTag(ctx, rel.Assets[i].URL)
	if err != nil {
		return Release{}, fmt.Errorf("release %s: %w", rel.Tag, err)
	}
	binaries, err := c.byTag(ctx, tag)
	if err != nil {
		return Release{}, err
	}
	rel.BinaryTag = tag
	rel.Assets = binaries.Assets
	return rel, nil
}

// Compare returns the commits on head that are not on base.
func (c *Client) Compare(ctx context.Context, base, head string) (Comparison, error) {
	if err := ValidateTag(base); err != nil {
		return Comparison{}, err
	}
	if err := ValidateTag(head); err != nil {
		return Comparison{}, err
	}
	raw, _, err := fetchJSON[apiComparison](ctx, c, c.base+repoPath+"/compare/"+url.PathEscape(base)+"..."+url.PathEscape(head))
	if err != nil {
		return Comparison{}, fmt.Errorf("compare %s...%s: %w", base, head, err)
	}
	out := Comparison{AheadBy: raw.AheadBy, Commits: make([]Commit, len(raw.Commits))}
	for i, cm := range raw.Commits {
		title, _, _ := strings.Cut(cm.Commit.Message, "\n")
		out.Commits[i] = Commit{SHA: cm.SHA, Title: strings.TrimSpace(title)}
	}
	return out, nil
}

// fetchBinaryTag reads nightly-tag.txt. It is a public download, not an API call, so it carries
// no token and does not count against the rate limit.
func (c *Client) fetchBinaryTag(ctx context.Context, assetURL string) (string, error) {
	if cached, ok := c.fresh(assetURL); ok {
		return cached.value.(string), nil
	}
	u, err := url.Parse(assetURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("bad asset URL %q", assetURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", nightlyAsset, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxTagFile+1))
	if err != nil {
		return "", err
	}
	tag := strings.TrimRight(string(raw), "\r\n")
	if !nightlyTagRE.MatchString(tag) || len(tag) > maxTagLen {
		return "", fmt.Errorf("%w: %s holds %q", ErrInvalidTag, nightlyAsset, tag)
	}
	c.store(assetURL, &cacheEntry{value: tag, fetched: c.now()})
	return tag, nil
}

// fetchJSON is a cached GET returning the decoded value and the rel="next" link. On a rate limit
// it falls back to a stale entry, if there is one.
func fetchJSON[T any](ctx context.Context, c *Client, rawURL string) (T, string, error) {
	var zero T
	if e, ok := c.fresh(rawURL); ok {
		return e.value.(T), e.next, nil
	}
	stale := c.entry(rawURL)
	serveStale := func(err error) (T, string, error) {
		if stale != nil {
			return stale.value.(T), stale.next, nil
		}
		return zero, "", err
	}
	if err := c.limited(); err != nil {
		return serveStale(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return zero, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "llama-gui")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if stale != nil && stale.etag != "" {
		req.Header.Set("If-None-Match", stale.etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotModified && stale != nil:
		c.store(rawURL, &cacheEntry{value: stale.value, etag: stale.etag, next: stale.next, fetched: c.now()})
		return stale.value.(T), stale.next, nil
	case resp.StatusCode == http.StatusNotFound:
		return zero, "", ErrNotFound
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if rl := c.rateLimit(resp, body); rl != nil {
			return serveStale(rl)
		}
		return zero, "", fmt.Errorf("github: HTTP %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return zero, "", fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}

	var value T
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&value); err != nil {
		return zero, "", fmt.Errorf("github: decode response: %w", err)
	}
	next := nextLink(resp.Header.Get("Link"))
	c.store(rawURL, &cacheEntry{value: value, etag: resp.Header.Get("ETag"), next: next, fetched: c.now()})
	return value, next, nil
}

func (c *Client) rateLimit(resp *http.Response, body []byte) *RateLimitError {
	retryAfter, retryErr := strconv.Atoi(resp.Header.Get("Retry-After"))
	if resp.StatusCode != http.StatusTooManyRequests && retryErr != nil &&
		resp.Header.Get("X-RateLimit-Remaining") != "0" &&
		!strings.Contains(strings.ToLower(string(body)), "rate limit") {
		return nil
	}
	reset := c.now().Add(limitFallback)
	if retryErr == nil {
		reset = c.now().Add(time.Duration(retryAfter) * time.Second)
	} else if unix, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		reset = time.Unix(unix, 0)
	}
	c.mu.Lock()
	c.limitedUntil = reset
	c.mu.Unlock()
	return &RateLimitError{Reset: reset}
}

// limited stops requests while a known rate limit lasts, as GitHub penalizes those who insist.
func (c *Client) limited() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now().Before(c.limitedUntil) {
		return &RateLimitError{Reset: c.limitedUntil}
	}
	return nil
}

func (c *Client) fresh(key string) (*cacheEntry, bool) {
	e := c.entry(key)
	if e == nil || c.now().Sub(e.fetched) >= cacheTTL {
		return nil, false
	}
	return e, true
}

func (c *Client) entry(key string) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cache[key]
}

func (c *Client) store(key string, e *cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = e
	if len(c.cache) <= maxCached {
		return
	}
	var oldest string
	for k, v := range c.cache {
		if oldest == "" || v.fetched.Before(c.cache[oldest].fetched) {
			oldest = k
		}
	}
	delete(c.cache, oldest)
}

// sameOrigin drops a next link that leaves the API host, which would leak the token.
func (c *Client) sameOrigin(link string) string {
	if link == "" {
		return ""
	}
	u, err := url.Parse(link)
	base, berr := url.Parse(c.base)
	if err != nil || berr != nil || u.Scheme != base.Scheme || u.Host != base.Host {
		return ""
	}
	return link
}

var linkRE = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

func nextLink(header string) string {
	if m := linkRE.FindStringSubmatch(header); m != nil {
		return m[1]
	}
	return ""
}
