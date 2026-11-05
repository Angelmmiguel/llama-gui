// Package store is the SQLite database: migrations, retention and every query.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Sentinel errors.
var (
	ErrNotFound      = errors.New("store: not found")
	ErrConflict      = errors.New("store: already exists")
	ErrInvalidCursor = errors.New("store: invalid cursor")
)

const (
	defaultLimit = 100
	maxLimit     = 1000
	busyTimeout  = "busy_timeout(5000)"
	pruneBatch   = 5000
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is an open database. It is safe for concurrent use.
type Store struct {
	// write has a single connection, so writers queue in Go rather than
	// contending on SQLite's lock. read serves queries concurrently, which WAL
	// allows alongside the writer.
	write *sql.DB
	read  *sql.DB
}

// Open opens the database at path, creating it if needed, and migrates it to
// the current schema. The parent directory must exist.
func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	dsn := func(query url.Values) string {
		return (&url.URL{Scheme: "file", Path: abs, RawQuery: query.Encode()}).String()
	}

	write, err := sql.Open("sqlite", dsn(url.Values{
		"_txlock": {"immediate"},
		"_pragma": {busyTimeout, "journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)"},
	}))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	write.SetMaxOpenConns(1)
	if err := migrate(write); err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	read, err := sql.Open("sqlite", dsn(url.Values{
		"_pragma": {busyTimeout, "query_only(1)"},
	}))
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return &Store{write: write, read: read}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return errors.Join(s.read.Close(), s.write.Close())
}

// migrate applies the embedded migrations newer than PRAGMA user_version, each
// in its own transaction. Files are named NNNN_description.sql, and NNNN is the
// schema version the file produces.
func migrate(db *sql.DB) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)

	var current int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&current); err != nil {
		return err
	}
	latest := 0
	for _, name := range names {
		version, _, ok := strings.Cut(filepath.Base(name), "_")
		if !ok {
			return fmt.Errorf("migration %s: name has no version", name)
		}
		n, err := strconv.Atoi(version)
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		latest = n
		if n <= current {
			continue
		}
		script, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if err := applyMigration(db, n, string(script)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	if current > latest {
		return fmt.Errorf("database schema %d is newer than this binary's %d", current, latest)
	}
	return nil
}

func applyMigration(db *sql.DB, version int, script string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(script); err != nil {
		return err
	}
	// PRAGMA takes no parameters; version is an integer from a file name.
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return err
	}
	return tx.Commit()
}

// GetKV returns the value of key, or ErrNotFound.
func (s *Store) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.read.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: get kv: %w", err)
	}
	return v, nil
}

// SetKV stores value under key, replacing any previous value.
func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("store: set kv: %w", err)
	}
	return nil
}

// Profile is a stored launch profile. The store treats Doc as opaque JSON;
// internal/profiles owns its shape.
type Profile struct {
	ID  string
	Doc json.RawMessage
}

// CreateProfile stores a new profile, or returns ErrConflict if the id exists.
func (s *Store) CreateProfile(ctx context.Context, p Profile) error {
	res, err := s.write.ExecContext(ctx,
		`INSERT INTO profiles (id, doc) VALUES (?, ?) ON CONFLICT (id) DO NOTHING`, p.ID, string(p.Doc))
	if err != nil {
		return fmt.Errorf("store: create profile: %w", err)
	}
	return oneRow(res, ErrConflict, "create profile")
}

// GetProfile returns a profile by id, or ErrNotFound.
func (s *Store) GetProfile(ctx context.Context, id string) (Profile, error) {
	p := Profile{ID: id}
	var doc string
	err := s.read.QueryRowContext(ctx, `SELECT doc FROM profiles WHERE id = ?`, id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("store: get profile: %w", err)
	}
	p.Doc = json.RawMessage(doc)
	return p, nil
}

// ListProfiles returns every profile, ordered by id.
func (s *Store) ListProfiles(ctx context.Context) ([]Profile, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id, doc FROM profiles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list profiles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Profile
	for rows.Next() {
		var p Profile
		var doc string
		if err := rows.Scan(&p.ID, &doc); err != nil {
			return nil, fmt.Errorf("store: list profiles: %w", err)
		}
		p.Doc = json.RawMessage(doc)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list profiles: %w", err)
	}
	return out, nil
}

// UpdateProfile replaces a profile's document, or returns ErrNotFound.
func (s *Store) UpdateProfile(ctx context.Context, p Profile) error {
	res, err := s.write.ExecContext(ctx, `UPDATE profiles SET doc = ? WHERE id = ?`, string(p.Doc), p.ID)
	if err != nil {
		return fmt.Errorf("store: update profile: %w", err)
	}
	return oneRow(res, ErrNotFound, "update profile")
}

// DeleteProfile removes a profile, or returns ErrNotFound.
func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	res, err := s.write.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete profile: %w", err)
	}
	return oneRow(res, ErrNotFound, "delete profile")
}

// oneRow returns none when the statement changed no row.
func oneRow(res sql.Result, none error, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	if n == 0 {
		return none
	}
	return nil
}

// Request is one proxied call. Timing fields are zero when upstream did not
// report them; Slot and TaskID are nil when unknown, because 0 is a real value.
type Request struct {
	ID                 int64
	StartedAt          time.Time
	Model              string
	Endpoint           string
	Method             string
	Status             int
	Slot               *int
	TaskID             *int64
	TTFTMs             float64
	TotalMs            float64
	PromptN            int
	CacheN             int
	PromptMs           float64
	PredictedN         int
	PredictedMs        float64
	PredictedPerSecond float64
	Stream             bool
	Error              string
	// Gaps is a JSON array of inter-token gaps, or nil.
	Gaps json.RawMessage
}

// StatusClass is the status's hundreds digit: 2 for any 2xx.
func (r Request) StatusClass() int { return r.Status / 100 }

// InsertRequest stores a finished request and returns its id.
func (s *Store) InsertRequest(ctx context.Context, r Request) (int64, error) {
	res, err := s.write.ExecContext(ctx, `INSERT INTO requests (
		started_at, model, endpoint, method, status, status_class, slot, task_id, ttft_ms, total_ms,
		prompt_n, cache_n, prompt_ms, predicted_n, predicted_ms, predicted_per_second, stream, error, gaps
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.StartedAt.UnixMicro(), r.Model, r.Endpoint, r.Method, r.Status, r.StatusClass(), r.Slot, r.TaskID,
		r.TTFTMs, r.TotalMs, r.PromptN, r.CacheN, r.PromptMs, r.PredictedN, r.PredictedMs, r.PredictedPerSecond,
		r.Stream, r.Error, nullJSON(r.Gaps))
	if err != nil {
		return 0, fmt.Errorf("store: insert request: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert request: %w", err)
	}
	return id, nil
}

// InsertRequestBody stores the captured bodies of an existing request,
// replacing any stored earlier.
func (s *Store) InsertRequestBody(ctx context.Context, requestID int64, request, response []byte) error {
	_, err := s.write.ExecContext(ctx, `INSERT INTO request_bodies (request_id, request, response) VALUES (?, ?, ?)
		ON CONFLICT (request_id) DO UPDATE SET request = excluded.request, response = excluded.response`,
		requestID, request, response)
	if err != nil {
		return fmt.Errorf("store: insert request body: %w", err)
	}
	return nil
}

// GetRequestBody returns a request's captured bodies, or ErrNotFound.
func (s *Store) GetRequestBody(ctx context.Context, requestID int64) (request, response []byte, err error) {
	err = s.read.QueryRowContext(ctx, `SELECT request, response FROM request_bodies WHERE request_id = ?`, requestID).
		Scan(&request, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: get request body: %w", err)
	}
	return request, response, nil
}

const requestColumns = `id, started_at, model, endpoint, method, status, slot, task_id, ttft_ms, total_ms,
	prompt_n, cache_n, prompt_ms, predicted_n, predicted_ms, predicted_per_second, stream, error, gaps`

func scanRequest(row interface{ Scan(...any) error }) (Request, error) {
	var (
		r         Request
		startedAt int64
		slot      sql.NullInt64
		taskID    sql.NullInt64
		gaps      sql.NullString
	)
	err := row.Scan(&r.ID, &startedAt, &r.Model, &r.Endpoint, &r.Method, &r.Status, &slot, &taskID,
		&r.TTFTMs, &r.TotalMs, &r.PromptN, &r.CacheN, &r.PromptMs, &r.PredictedN, &r.PredictedMs,
		&r.PredictedPerSecond, &r.Stream, &r.Error, &gaps)
	if err != nil {
		return Request{}, err
	}
	r.StartedAt = time.UnixMicro(startedAt).UTC()
	if slot.Valid {
		n := int(slot.Int64)
		r.Slot = &n
	}
	if taskID.Valid {
		r.TaskID = &taskID.Int64
	}
	if gaps.Valid {
		r.Gaps = json.RawMessage(gaps.String)
	}
	return r, nil
}

// GetRequest returns a request by id, or ErrNotFound.
func (s *Store) GetRequest(ctx context.Context, id int64) (Request, error) {
	r, err := scanRequest(s.read.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM requests WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("store: get request: %w", err)
	}
	return r, nil
}

// RequestFilter narrows ListRequests. Zero fields do not filter.
type RequestFilter struct {
	Since time.Time // inclusive
	Until time.Time // exclusive
	// Endpoint is the exact value of the request's Endpoint field, or several separated by commas.
	Endpoint string
	Model    string
	// StatusClass is the status's hundreds digit: 2 for any 2xx.
	StatusClass int
	// Limit defaults to 100 and is capped at 1000.
	Limit int
	// Cursor is the next-page value from the previous call.
	Cursor string
}

// ListRequests returns matching requests, newest first, and a cursor for the
// next page, empty on the last one.
func (s *Store) ListRequests(ctx context.Context, f RequestFilter) ([]Request, string, error) {
	where, args := requestWhere(f)
	if f.Cursor != "" {
		startedAt, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where, args = append(where, `(started_at, id) < (?, ?)`), append(args, startedAt, id)
	}
	limit := pageSize(f.Limit)
	rows, err := s.read.QueryContext(ctx, `SELECT `+requestColumns+` FROM requests`+whereClause(where)+
		` ORDER BY started_at DESC, id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list requests: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: list requests: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: list requests: %w", err)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	last := out[limit-1]
	return out, encodeCursor(last.StartedAt.UnixMicro(), last.ID), nil
}

// appendAnyOf narrows column to the comma-separated values in list. Column is a
// literal from this package.
func appendAnyOf(where []string, args []any, column, list string) ([]string, []any) {
	if list == "" {
		return where, args
	}
	values := strings.Split(list, ",")
	for _, v := range values {
		args = append(args, v)
	}
	return append(where, column+` IN (?`+strings.Repeat(`,?`, len(values)-1)+`)`), args
}

func requestWhere(f RequestFilter) (where []string, args []any) {
	if !f.Since.IsZero() {
		where, args = append(where, `started_at >= ?`), append(args, f.Since.UnixMicro())
	}
	if !f.Until.IsZero() {
		where, args = append(where, `started_at < ?`), append(args, f.Until.UnixMicro())
	}
	where, args = appendAnyOf(where, args, "endpoint", f.Endpoint)
	if f.Model != "" {
		where, args = append(where, `model = ?`), append(args, f.Model)
	}
	if f.StatusClass != 0 {
		where, args = append(where, `status_class = ?`), append(args, f.StatusClass)
	}
	return where, args
}

// RequestsForSummary returns up to max matching requests without their gaps,
// newest first, for aggregating in Go. Limit and Cursor are ignored.
func (s *Store) RequestsForSummary(ctx context.Context, f RequestFilter, maxRows int) ([]Request, error) {
	where, args := requestWhere(f)
	columns := strings.Replace(requestColumns, "gaps", "NULL", 1)
	rows, err := s.read.QueryContext(ctx, `SELECT `+columns+` FROM requests`+whereClause(where)+
		` ORDER BY started_at DESC, id DESC LIMIT ?`, append(args, maxRows)...)
	if err != nil {
		return nil, fmt.Errorf("store: summarise requests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("store: summarise requests: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: summarise requests: %w", err)
	}
	return out, nil
}

// LogLine is one line of router output. Raw is always kept, so a line that
// could not be parsed still has its text.
type LogLine struct {
	ID      int64
	TS      time.Time
	Level   string
	Source  string
	Model   string
	Message string
	Raw     string
}

// InsertLogLines stores a batch in one transaction, in order, and sets each
// line's ID.
func (s *Store) InsertLogLines(ctx context.Context, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: insert log lines: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO log_lines (ts, level, source, model, message, raw) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: insert log lines: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	ids := make([]int64, len(lines))
	for i, l := range lines {
		res, err := stmt.ExecContext(ctx, l.TS.UnixMicro(), l.Level, l.Source, l.Model, l.Message, l.Raw)
		if err != nil {
			return fmt.Errorf("store: insert log lines: %w", err)
		}
		if ids[i], err = res.LastInsertId(); err != nil {
			return fmt.Errorf("store: insert log lines: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: insert log lines: %w", err)
	}
	for i := range lines {
		lines[i].ID = ids[i]
	}
	return nil
}

// LogFilter narrows ListLogLines. Zero fields do not filter.
type LogFilter struct {
	Since time.Time // inclusive
	Until time.Time // exclusive
	// Level and Source take one value, or several separated by commas.
	Level  string
	Source string
	Model  string
	// Query is a case-insensitive (ASCII) substring of the message or raw line.
	Query string
	// Limit defaults to 100 and is capped at 1000.
	Limit int
	// Cursor is the next-page value from the previous call.
	Cursor string
}

// ListLogLines returns matching lines, newest first, and a cursor for the next
// page, empty on the last one.
func (s *Store) ListLogLines(ctx context.Context, f LogFilter) ([]LogLine, string, error) {
	where, args := logWhere(f)
	if f.Cursor != "" {
		_, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where, args = append(where, `id < ?`), append(args, id)
	}
	limit := pageSize(f.Limit)
	out, err := s.queryLogLines(ctx, whereClause(where)+` ORDER BY id DESC LIMIT ?`, append(args, limit+1))
	if err != nil {
		return nil, "", err
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	return out, encodeCursor(0, out[limit-1].ID), nil
}

// LogLinesAfter returns matching lines with an id above afterID, oldest first,
// for walking the whole log forwards. Limit is as in ListLogLines; Cursor is
// ignored.
func (s *Store) LogLinesAfter(ctx context.Context, f LogFilter, afterID int64) ([]LogLine, error) {
	where, args := logWhere(f)
	where, args = append(where, `id > ?`), append(args, afterID)
	return s.queryLogLines(ctx, whereClause(where)+` ORDER BY id LIMIT ?`, append(args, pageSize(f.Limit)))
}

func logWhere(f LogFilter) (where []string, args []any) {
	if !f.Since.IsZero() {
		where, args = append(where, `ts >= ?`), append(args, f.Since.UnixMicro())
	}
	if !f.Until.IsZero() {
		where, args = append(where, `ts < ?`), append(args, f.Until.UnixMicro())
	}
	where, args = appendAnyOf(where, args, "level", f.Level)
	where, args = appendAnyOf(where, args, "source", f.Source)
	if f.Model != "" {
		where, args = append(where, `model = ?`), append(args, f.Model)
	}
	if f.Query != "" {
		pattern := "%" + likeEscaper.Replace(f.Query) + "%"
		where = append(where, `(message LIKE ? ESCAPE '\' OR raw LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}
	return where, args
}

func (s *Store) queryLogLines(ctx context.Context, tail string, args []any) ([]LogLine, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT id, ts, level, source, model, message, raw FROM log_lines`+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list log lines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []LogLine
	for rows.Next() {
		var l LogLine
		var ts int64
		if err := rows.Scan(&l.ID, &ts, &l.Level, &l.Source, &l.Model, &l.Message, &l.Raw); err != nil {
			return nil, fmt.Errorf("store: list log lines: %w", err)
		}
		l.TS = time.UnixMicro(ts).UTC()
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list log lines: %w", err)
	}
	return out, nil
}

// LogVolumeBucket counts one level's lines in one minute.
type LogVolumeBucket struct {
	Minute time.Time
	Level  string
	Count  int64
}

// LogVolume counts lines per minute and level since the given time, oldest first.
func (s *Store) LogVolume(ctx context.Context, since time.Time) ([]LogVolumeBucket, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT ts / 60000000 AS minute, level, COUNT(*) FROM log_lines
		WHERE ts >= ? GROUP BY minute, level ORDER BY minute, level`, since.UnixMicro())
	if err != nil {
		return nil, fmt.Errorf("store: log volume: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []LogVolumeBucket{}
	for rows.Next() {
		var b LogVolumeBucket
		var minute int64
		if err := rows.Scan(&minute, &b.Level, &b.Count); err != nil {
			return nil, fmt.Errorf("store: log volume: %w", err)
		}
		b.Minute = time.Unix(minute*60, 0).UTC()
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: log volume: %w", err)
	}
	return out, nil
}

// CountLogLevels counts lines per level since the given time.
func (s *Store) CountLogLevels(ctx context.Context, since time.Time) (map[string]int64, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT level, COUNT(*) FROM log_lines WHERE ts >= ? GROUP BY level`, since.UnixMicro())
	if err != nil {
		return nil, fmt.Errorf("store: count log levels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var level string
		var n int64
		if err := rows.Scan(&level, &n); err != nil {
			return nil, fmt.Errorf("store: count log levels: %w", err)
		}
		out[level] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: count log levels: %w", err)
	}
	return out, nil
}

// Event is something derived from the logs or the supervisor, for the timeline.
type Event struct {
	ID       int64
	TS       time.Time
	Kind     string
	Severity string
	Title    string
	// Meta is a JSON document, or nil.
	Meta json.RawMessage
}

// InsertEvent stores an event and returns its id.
func (s *Store) InsertEvent(ctx context.Context, e Event) (int64, error) {
	res, err := s.write.ExecContext(ctx, `INSERT INTO events (ts, kind, severity, title, meta) VALUES (?, ?, ?, ?, ?)`,
		e.TS.UnixMicro(), e.Kind, e.Severity, e.Title, nullJSON(e.Meta))
	if err != nil {
		return 0, fmt.Errorf("store: insert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert event: %w", err)
	}
	return id, nil
}

// ListEvents returns the events at or after since, newest first, up to the
// newest 1000. A zero since does not filter.
func (s *Store) ListEvents(ctx context.Context, since time.Time) ([]Event, error) {
	var (
		where []string
		args  []any
	)
	if !since.IsZero() {
		where, args = append(where, `ts >= ?`), append(args, since.UnixMicro())
	}
	rows, err := s.read.QueryContext(ctx, `SELECT id, ts, kind, severity, title, meta FROM events`+
		whereClause(where)+` ORDER BY ts DESC, id DESC LIMIT ?`, append(args, maxLimit)...)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var e Event
		var ts int64
		var meta sql.NullString
		if err := rows.Scan(&e.ID, &ts, &e.Kind, &e.Severity, &e.Title, &meta); err != nil {
			return nil, fmt.Errorf("store: list events: %w", err)
		}
		e.TS = time.UnixMicro(ts).UTC()
		if meta.Valid {
			e.Meta = json.RawMessage(meta.String)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	return out, nil
}

// Prune deletes requests (with their bodies), log lines and events older than
// before, and returns how many rows of those three tables went. It deletes in
// small transactions so a big backlog doesn't hold the writer for long.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	cutoff := before.UnixMicro()
	var total int64
	for _, query := range []string{
		`DELETE FROM requests WHERE id IN (SELECT id FROM requests WHERE started_at < ? LIMIT ?)`,
		`DELETE FROM log_lines WHERE id IN (SELECT id FROM log_lines WHERE ts < ? LIMIT ?)`,
		`DELETE FROM events WHERE id IN (SELECT id FROM events WHERE ts < ? LIMIT ?)`,
	} {
		for {
			res, err := s.write.ExecContext(ctx, query, cutoff, pruneBatch)
			if err != nil {
				return total, fmt.Errorf("store: prune: %w", err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return total, fmt.Errorf("store: prune: %w", err)
			}
			total += n
			if n < pruneBatch {
				break
			}
		}
	}
	return total, nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// whereClause joins fixed condition strings; values stay in parameters.
func whereClause(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

func pageSize(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	return min(limit, maxLimit)
}

func nullJSON(doc json.RawMessage) any {
	if doc == nil {
		return nil
	}
	return string(doc)
}

// encodeCursor makes the opaque page token: the sort key of the last row.
func encodeCursor(ts, id int64) string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d:%d", ts, id))
}

func decodeCursor(cursor string) (ts, id int64, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, 0, ErrInvalidCursor
	}
	a, b, ok := strings.Cut(string(raw), ":")
	if !ok {
		return 0, 0, ErrInvalidCursor
	}
	if ts, err = strconv.ParseInt(a, 10, 64); err != nil {
		return 0, 0, ErrInvalidCursor
	}
	if id, err = strconv.ParseInt(b, 10, 64); err != nil {
		return 0, 0, ErrInvalidCursor
	}
	return ts, id, nil
}
