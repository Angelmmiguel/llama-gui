package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBenchRuns(t *testing.T) {
	s, ctx := open(t), context.Background()
	t0 := time.UnixMicro(1_700_000_000_000_000)

	a, err := s.InsertBenchRun(ctx, BenchRun{StartedAt: t0, State: "running", BuildID: "b1", Model: "m1", Setup: []byte(`{"reps":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.InsertBenchRun(ctx, BenchRun{StartedAt: t0.Add(time.Hour), State: "done", BuildID: "b2", Model: "m2"})

	err = s.UpdateBenchRun(ctx, BenchRun{ID: a, FinishedAt: t0.Add(time.Minute), State: "done", Results: []byte(`[{"test":"tg128"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBenchRun(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "done" || string(got.Results) != `[{"test":"tg128"}]` || string(got.Setup) != `{"reps":1}` ||
		!got.FinishedAt.Equal(t0.Add(time.Minute)) || !got.StartedAt.Equal(t0) || got.BuildID != "b1" {
		t.Errorf("got %+v", got)
	}

	all, _ := s.ListBenchRuns(ctx, "", 10)
	if len(all) != 2 || all[0].ID != b {
		t.Errorf("all = %+v, want newest first", all)
	}
	if one, _ := s.ListBenchRuns(ctx, "m1", 10); len(one) != 1 || one[0].ID != a {
		t.Errorf("m1 = %+v", one)
	}
	if !all[0].FinishedAt.IsZero() && all[0].ID == b {
		t.Errorf("unfinished run has FinishedAt %v", all[0].FinishedAt)
	}

	if _, err := s.GetBenchRun(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v", err)
	}
	if err := s.UpdateBenchRun(ctx, BenchRun{ID: 99, State: "done"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v", err)
	}
}
