package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Errors from the download queue.
var (
	ErrNoSpace   = errors.New("not enough free disk space")
	ErrExists    = errors.New("model already downloaded")
	ErrDuplicate = errors.New("already queued or downloading")
	ErrNoJob     = errors.New("no such download")
	ErrClosed    = errors.New("download queue is closed")
	ErrChecksum  = errors.New("checksum mismatch, partial file discarded")
	ErrNoModel   = errors.New("a download needs at least one model file besides mmproj")
)

// diskReserve is kept free on top of what a download needs.
const diskReserve = 1 << 30

const maxHistory = 50

// JobState is where a download is in its life.
type JobState string

// Download states.
const (
	JobQueued      JobState = "queued"
	JobDownloading JobState = "downloading"
	JobDone        JobState = "done"
	JobFailed      JobState = "failed"
	JobCanceled    JobState = "canceled"
)

// Job is a snapshot of a download.
type Job struct {
	ID          string   `json:"id"`
	Repo        string   `json:"repo"`
	Variant     string   `json:"variant"`
	Files       []string `json:"files"`
	State       JobState `json:"state"`
	Done        int64    `json:"done"`
	Total       int64    `json:"total"`
	BytesPerSec int64    `json:"bytes_per_sec"`
	ETA         int64    `json:"eta_seconds"` // zero when unknown
	Error       string   `json:"error,omitempty"`
}

type job struct {
	Job
	files    []File // Name is the repo path
	final    string // model path: a file in the root, or a directory
	work     string // where it is assembled
	isDir    bool
	cancel   context.CancelFunc
	userStop bool
	rmParts  bool
	lastT    time.Time
	lastB    int64
}

// Queue downloads models one at a time into modelsDir in the router's layout. Partial files
// resume across restarts and nothing appears under its final name until it is verified.
type Queue struct {
	dir      string
	client   *Client
	now      func() time.Time
	onFinish func()
	free     func(string) (int64, error)

	mu     sync.Mutex
	jobs   []*job
	seq    int
	closed bool
	wake   chan struct{}
	ctx    context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
}

// NewQueue starts a queue. now may be nil; onFinish, if set, runs after each completed download
// (the coordinator uses it to ask the router to rescan /models).
func NewQueue(modelsDir string, client *Client, now func() time.Time, onFinish func()) *Queue {
	if now == nil {
		now = time.Now
	}
	ctx, stop := context.WithCancel(context.Background())
	q := &Queue{dir: modelsDir, client: client, now: now, onFinish: onFinish, free: freeBytes,
		wake: make(chan struct{}, 1), ctx: ctx, stop: stop}
	q.wg.Add(1)
	go q.loop()
	return q
}

// Close stops the running download, keeping its partial files for a later resume, and waits for the
// worker. It is safe to call more than once.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.stop()
	q.wg.Wait()
}

// Enqueue validates the files of repo, checks the disk, and queues them as one job. Multiple files
// (shards, or a model plus mmproj) land in a directory; a single file lands in the root.
func (q *Queue) Enqueue(ctx context.Context, repo string, files ...string) (Job, error) {
	if !ValidRepo(repo) {
		return Job{}, ErrInvalidRepo
	}
	if len(files) == 0 {
		return Job{}, fmt.Errorf("%w: no files", ErrInvalidName)
	}
	info, err := q.client.Repo(ctx, repo)
	if err != nil {
		return Job{}, err
	}
	known := map[string]File{}
	for _, v := range info.Variants {
		for _, f := range v.Files {
			known[f.Name] = f
		}
	}
	for _, f := range info.MMProj {
		known[f.Name] = f
	}
	j := &job{}
	j.Repo = repo
	var mainBase string
	seen := map[string]bool{}
	for _, name := range files {
		base, err := validRepoFile(name)
		if err != nil {
			return Job{}, err
		}
		f, ok := known[name]
		if !ok || f.Size <= 0 {
			return Job{}, fmt.Errorf("%w: %s is not a GGUF file of %s", ErrNotFound, name, repo)
		}
		if seen[base] {
			return Job{}, fmt.Errorf("%w: %q listed twice", ErrInvalidName, base)
		}
		seen[base] = true
		if !isMMProjName(base) && mainBase == "" || !isMMProjName(base) && base < mainBase {
			mainBase = base
		}
		j.files = append(j.files, f)
		j.Files = append(j.Files, name)
		j.Total += f.Size
	}
	if mainBase == "" {
		return Job{}, ErrNoModel
	}
	j.Variant = quantFromName(mainBase)
	j.isDir = len(files) > 1
	if j.isDir {
		stem := strings.TrimSuffix(mainBase, ".gguf")
		if loc := shardRe.FindStringIndex(mainBase); loc != nil {
			stem = mainBase[:loc[0]]
		}
		j.final = filepath.Join(q.dir, stem)
		j.work = filepath.Join(q.dir, "."+stem+".partial")
	} else {
		j.final = filepath.Join(q.dir, mainBase)
		j.work = j.final + ".partial"
	}
	if _, err := os.Lstat(j.final); err == nil {
		return Job{}, fmt.Errorf("%w: %s", ErrExists, filepath.Base(j.final))
	}
	if err := q.checkSpace(j); err != nil {
		return Job{}, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return Job{}, ErrClosed
	}
	for _, o := range q.jobs {
		if o.final == j.final && (o.State == JobQueued || o.State == JobDownloading) {
			return Job{}, fmt.Errorf("%w: %s", ErrDuplicate, filepath.Base(j.final))
		}
	}
	q.seq++
	j.ID = fmt.Sprintf("dl-%d", q.seq)
	j.State = JobQueued
	q.jobs = append(q.jobs, j)
	q.trim()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return j.Job, nil
}

// checkSpace refuses when the free space cannot hold what is still to download plus a reserve.
func (q *Queue) checkSpace(j *job) error {
	remaining := j.Total
	for _, f := range j.files {
		if fi, err := os.Stat(j.partPath(path.Base(f.Name))); err == nil {
			remaining -= min(fi.Size(), f.Size)
		}
	}
	free, err := q.free(q.dir)
	if err != nil {
		return err
	}
	if free < remaining+diskReserve {
		return fmt.Errorf("%w: %d MiB free, %d MiB needed plus 1 GiB spare", ErrNoSpace, free>>20, remaining>>20)
	}
	return nil
}

func (j *job) partPath(base string) string {
	if j.isDir {
		return filepath.Join(j.work, base+".partial")
	}
	return j.work
}

// trim drops the oldest finished jobs beyond maxHistory. q.mu must be held.
func (q *Queue) trim() {
	for len(q.jobs) > maxHistory {
		i := 0
		for i < len(q.jobs) && (q.jobs[i].State == JobQueued || q.jobs[i].State == JobDownloading) {
			i++
		}
		if i == len(q.jobs) {
			return
		}
		q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
	}
}

// List returns snapshots of all known jobs, oldest first.
func (q *Queue) List() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, len(q.jobs))
	for i, j := range q.jobs {
		out[i] = j.Job
		out[i].Files = append([]string(nil), j.Files...)
	}
	return out
}

// Cancel stops a queued or running download. With removePartials the partial files are deleted;
// otherwise they stay so the same download can resume later.
func (q *Queue) Cancel(id string, removePartials bool) error {
	q.mu.Lock()
	var j *job
	for _, o := range q.jobs {
		if o.ID == id {
			j = o
		}
	}
	if j == nil {
		q.mu.Unlock()
		return ErrNoJob
	}
	switch j.State {
	case JobQueued:
		j.State = JobCanceled
		q.mu.Unlock()
		if removePartials {
			_ = os.RemoveAll(j.work)
		}
		return nil
	case JobDownloading:
		j.userStop, j.rmParts = true, removePartials
		cancel := j.cancel
		q.mu.Unlock()
		cancel()
		return nil
	}
	q.mu.Unlock()
	return nil
}

func (q *Queue) loop() {
	defer q.wg.Done()
	for {
		var j *job
		q.mu.Lock()
		for _, o := range q.jobs {
			if o.State == JobQueued {
				j = o
				break
			}
		}
		if j != nil {
			ctx, cancel := context.WithCancel(q.ctx)
			j.cancel, j.State, j.lastT = cancel, JobDownloading, q.now()
			q.mu.Unlock()
			err := q.run(ctx, j)
			cancel()
			q.finish(j, err)
			continue
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-q.ctx.Done():
			return
		}
	}
}

func (q *Queue) finish(j *job, err error) {
	q.mu.Lock()
	j.BytesPerSec, j.ETA = 0, 0
	switch {
	case err == nil:
		j.State, j.Done = JobDone, j.Total
	case j.userStop || q.ctx.Err() != nil:
		j.State = JobCanceled
	default:
		j.State, j.Error = JobFailed, err.Error()
	}
	rm := j.userStop && j.rmParts
	q.mu.Unlock()
	if rm {
		_ = os.RemoveAll(j.work)
	}
	if err == nil && q.onFinish != nil {
		q.onFinish()
	}
}

func (q *Queue) run(ctx context.Context, j *job) error {
	if err := q.checkSpace(j); err != nil {
		return err
	}
	if j.isDir {
		if err := os.MkdirAll(j.work, 0o755); err != nil { //nolint:gosec // holds no secrets
			return err
		}
	}
	var base int64
	for _, f := range j.files {
		if err := q.fetch(ctx, j, f, base); err != nil {
			return err
		}
		base += f.Size
	}
	if !j.isDir {
		return renameNoReplace(j.work, j.final)
	}
	for _, f := range j.files {
		b := path.Base(f.Name)
		if err := os.Rename(j.partPath(b), filepath.Join(j.work, b)); err != nil {
			return err
		}
	}
	return renameNoReplace(j.work, j.final)
}

// renameNoReplace renames src to dst unless dst appeared in the meantime.
func renameNoReplace(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%w: %s", ErrExists, filepath.Base(dst))
	}
	return os.Rename(src, dst)
}

// progress records bytes written for job j, whose earlier files total base bytes.
func (q *Queue) progress(j *job, base, inFile int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j.Done = base + inFile
	now := q.now()
	if dt := now.Sub(j.lastT); dt >= time.Second {
		inst := float64(j.Done-j.lastB) / dt.Seconds()
		if j.BytesPerSec == 0 {
			j.BytesPerSec = int64(inst)
		} else {
			j.BytesPerSec = int64(0.7*float64(j.BytesPerSec) + 0.3*inst)
		}
		j.lastT, j.lastB = now, j.Done
		j.ETA = 0
		if j.BytesPerSec > 0 {
			j.ETA = (j.Total - j.Done) / j.BytesPerSec
		}
	}
}

// fetch downloads one file into its partial path, resuming from what is already there.
func (q *Queue) fetch(ctx context.Context, j *job, f File, base int64) error {
	part := j.partPath(path.Base(f.Name))
	h := sha256.New()
	offset, err := resumeOffset(part, f.Size, f.SHA256 != "", h)
	if err != nil {
		return err
	}
	q.progress(j, base, offset)

	if offset < f.Size {
		hdr := http.Header{}
		if offset > 0 {
			hdr.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := q.client.request(ctx, q.client.resolveURL(j.Repo, f.Name), hdr)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
		if offset > 0 && resp.StatusCode != http.StatusPartialContent {
			// The server ignored Range and is sending the whole file again.
			offset, flags = 0, os.O_WRONLY|os.O_CREATE|os.O_TRUNC
			h.Reset()
		}
		out, err := os.OpenFile(part, flags, 0o644) //nolint:gosec // model files are not secret
		if err != nil {
			return err
		}
		n, err := q.copy(out, resp.Body, h, f.Size-offset+1, func(n int64) { q.progress(j, base, offset+n) })
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		offset += n
	}
	if offset != f.Size {
		if offset > f.Size {
			_ = os.Remove(part)
		}
		return fmt.Errorf("%s: got %d bytes, expected %d", path.Base(f.Name), offset, f.Size)
	}
	if f.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), f.SHA256) {
		_ = os.Remove(part)
		return fmt.Errorf("%s: %w", path.Base(f.Name), ErrChecksum)
	}
	return nil
}

// resumeOffset returns the usable size of an existing partial file, feeding it to h when a checksum
// is wanted. A partial larger than expected is discarded.
func resumeOffset(part string, size int64, hashIt bool, h hash.Hash) (int64, error) {
	fi, err := os.Stat(part)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if fi.Size() > size {
		return 0, os.Remove(part)
	}
	if hashIt {
		f, err := os.Open(part)
		if err != nil {
			return 0, err
		}
		defer func() { _ = f.Close() }()
		if _, err := io.Copy(h, f); err != nil {
			return 0, err
		}
	}
	return fi.Size(), nil
}

// copy streams at most limit bytes and fails if the source has more, which a well-behaved server
// never sends. It returns how many bytes were written to w.
func (q *Queue) copy(w io.Writer, r io.Reader, h hash.Hash, limit int64, report func(int64)) (int64, error) {
	buf := make([]byte, 256<<10)
	var n int64
	for {
		m, err := r.Read(buf)
		if m > 0 {
			if n+int64(m) >= limit {
				return n, errors.New("server sent more data than the file's size")
			}
			if _, werr := w.Write(buf[:m]); werr != nil {
				return n, werr
			}
			h.Write(buf[:m])
			n += int64(m)
			report(n)
		}
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}
