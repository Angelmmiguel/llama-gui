package builds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/releases"
)

// ErrDigestMismatch means a download does not match the digest GitHub reports.
var ErrDigestMismatch = errors.New("digest mismatch")

var (
	errStalled   = errors.New("download stalled")
	assetNameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	sha256HexRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	contentRange = regexp.MustCompile(`^bytes (\d+)-`)
)

const maxDownloadBytes = 16 << 30

func (m *Manager) partialPath(a releases.Asset) (string, error) {
	if !assetNameRE.MatchString(a.Name) {
		return "", fmt.Errorf("bad asset name %q", a.Name)
	}
	return filepath.Join(m.tmpDir(), a.Name+".part"), nil
}

// download brings the asset to its .part file, resuming what an earlier attempt left there.
func (m *Manager) download(ctx context.Context, j *job, a releases.Asset) (string, error) {
	p, err := m.partialPath(a)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("bad asset URL %q", a.URL)
	}
	base := j.snapshot().Done
	for range 2 {
		var offset int64
		if info, err := os.Stat(p); err == nil {
			offset = info.Size()
		}
		if a.Size > 0 && offset > a.Size {
			if err := os.Remove(p); err != nil {
				return "", err
			}
			offset = 0
		}
		j.setDone(base+offset, m.now())
		if a.Size > 0 && offset == a.Size {
			return p, nil
		}
		restart, err := m.fetch(ctx, j, a, p, offset)
		if err != nil {
			return "", fmt.Errorf("download %s: %w", a.Name, err)
		}
		if !restart {
			return p, nil
		}
	}
	return "", fmt.Errorf("download %s: server refuses the range", a.Name)
}

// fetch appends to the partial file from offset. It reports true when the partial was found to
// be unusable, was removed, and the download has to start over.
func (m *Manager) fetch(ctx context.Context, j *job, a releases.Asset, p string, offset int64) (bool, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(m.stallTimeout, func() { cancel(errStalled) })
	defer idle.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "llama-gui")
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return false, causeOf(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusOK:
		flags |= os.O_TRUNC
		j.setDone(j.snapshot().Done-offset, m.now())
		offset = 0
	case http.StatusPartialContent:
		match := contentRange.FindStringSubmatch(resp.Header.Get("Content-Range"))
		if match == nil || match[1] != strconv.FormatInt(offset, 10) {
			return false, fmt.Errorf("unexpected Content-Range %q", resp.Header.Get("Content-Range"))
		}
		flags |= os.O_APPEND
	case http.StatusRequestedRangeNotSatisfiable:
		return true, os.Remove(p)
	default:
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(p, flags, 0o600)
	if err != nil {
		return false, err
	}
	limit := int64(maxDownloadBytes)
	if a.Size > 0 {
		limit = a.Size
	}
	written, err := io.Copy(f, &progressReader{
		r: io.LimitReader(resp.Body, limit-offset+1),
		onRead: func(n int) {
			idle.Reset(m.stallTimeout)
			j.addDone(int64(n), m.now())
		},
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, causeOf(ctx, err)
	}
	total := offset + written
	switch {
	case total > limit:
		_ = os.Remove(p)
		return false, fmt.Errorf("server sent more than the expected %d bytes", limit)
	case a.Size > 0 && total < a.Size:
		return false, fmt.Errorf("short download: %d of %d bytes", total, a.Size)
	}
	return false, nil
}

// causeOf prefers the reason the context ended over the read error it produced.
func causeOf(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

type progressReader struct {
	r      io.Reader
	onRead func(int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.onRead(n)
	}
	return n, err
}

// verifyDigest checks GitHub's "sha256:<hex>" digest, which old assets lack, and removes the
// file when it does not match, so that the next attempt does not resume from bad bytes.
func (m *Manager) verifyDigest(ctx context.Context, p string, a releases.Asset) error {
	if a.Digest == "" {
		m.log.Info("asset has no digest to verify", "asset", a.Name)
		return nil
	}
	algo, want, _ := strings.Cut(a.Digest, ":")
	if algo != "sha256" {
		m.log.Warn("asset has an unknown digest algorithm", "asset", a.Name, "digest", a.Digest)
		return nil
	}
	if !sha256HexRE.MatchString(want) {
		return fmt.Errorf("asset %s has a malformed digest %q", a.Name, a.Digest)
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = os.Remove(p)
		return fmt.Errorf("%w: %s is sha256:%s, expected %s", ErrDigestMismatch, a.Name, got, a.Digest)
	}
	return nil
}
