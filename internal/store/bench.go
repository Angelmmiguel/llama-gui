package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// BenchRun is one llama-bench run. Setup and Results are JSON owned by the
// bench package; FinishedAt is zero while the run is going.
type BenchRun struct {
	ID         int64
	StartedAt  time.Time
	FinishedAt time.Time
	State      string
	BuildID    string
	Model      string
	Setup      json.RawMessage
	Results    json.RawMessage
	Error      string
}

// InsertBenchRun stores a new run and returns its id.
func (s *Store) InsertBenchRun(ctx context.Context, r BenchRun) (int64, error) {
	res, err := s.write.ExecContext(ctx,
		`INSERT INTO bench_runs (started_at, finished_at, state, build_id, model, setup, results, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.StartedAt.UnixMicro(), nullTime(r.FinishedAt), r.State, r.BuildID, r.Model,
		jsonOr(r.Setup, "{}"), jsonOr(r.Results, "[]"), r.Error)
	if err != nil {
		return 0, fmt.Errorf("store: insert bench run: %w", err)
	}
	return res.LastInsertId()
}

// UpdateBenchRun replaces the mutable fields of run r.ID, or returns ErrNotFound.
func (s *Store) UpdateBenchRun(ctx context.Context, r BenchRun) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE bench_runs SET finished_at = ?, state = ?, results = ?, error = ? WHERE id = ?`,
		nullTime(r.FinishedAt), r.State, jsonOr(r.Results, "[]"), r.Error, r.ID)
	if err != nil {
		return fmt.Errorf("store: update bench run: %w", err)
	}
	return oneRow(res, ErrNotFound, "update bench run")
}

// GetBenchRun returns a run with its results, or ErrNotFound.
func (s *Store) GetBenchRun(ctx context.Context, id int64) (BenchRun, error) {
	r, err := scanBenchRun(s.read.QueryRowContext(ctx, benchSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return BenchRun{}, ErrNotFound
	}
	return r, err
}

// ListBenchRuns returns the newest runs first, for one model or, when model is
// empty, all of them.
func (s *Store) ListBenchRuns(ctx context.Context, model string, limit int) ([]BenchRun, error) {
	q, args := benchSelect, []any{}
	if model != "" {
		q += ` WHERE model = ?`
		args = append(args, model)
	}
	q += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	rows, err := s.read.QueryContext(ctx, q, append(args, pageSize(limit))...)
	if err != nil {
		return nil, fmt.Errorf("store: list bench runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BenchRun
	for rows.Next() {
		r, err := scanBenchRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const benchSelect = `SELECT id, started_at, finished_at, state, build_id, model, setup, results, error FROM bench_runs`

func scanBenchRun(row interface{ Scan(...any) error }) (BenchRun, error) {
	var (
		r        BenchRun
		started  int64
		finished sql.NullInt64
		setup    string
		results  string
	)
	if err := row.Scan(&r.ID, &started, &finished, &r.State, &r.BuildID, &r.Model, &setup, &results, &r.Error); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		return r, fmt.Errorf("store: scan bench run: %w", err)
	}
	r.StartedAt = time.UnixMicro(started)
	if finished.Valid {
		r.FinishedAt = time.UnixMicro(finished.Int64)
	}
	r.Setup, r.Results = json.RawMessage(setup), json.RawMessage(results)
	return r, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMicro()
}

func jsonOr(doc json.RawMessage, empty string) string {
	if len(doc) == 0 {
		return empty
	}
	return string(doc)
}
