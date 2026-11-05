package builds

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/hardware"
	"github.com/Angelmmiguel/llama-gui/internal/releases"
	"github.com/Angelmmiguel/llama-gui/internal/store"
)

type fakeResolver struct {
	stable, nightly string
	err             error
	calls           atomic.Int32
}

func (f *fakeResolver) ResolveVersion(_ context.Context, version string) (releases.Release, error) {
	f.calls.Add(1)
	if f.err != nil {
		return releases.Release{}, f.err
	}
	if version == "stable" {
		return releases.Release{Tag: "v1", BinaryTag: f.stable, Channel: releases.ChannelStable}, nil
	}
	return releases.Release{Tag: f.nightly, BinaryTag: f.nightly, Channel: releases.ChannelNightly}, nil
}

type updateHarness struct {
	res       *fakeResolver
	active    Manifest
	installed []string
	activated []string
	inFlight  atomic.Int32
	installEr error
	jobState  JobState
}

func (h *updateHarness) checker(policy string) *UpdateChecker {
	return NewUpdateChecker(UpdateOptions{
		Policy:   policy,
		Releases: h.res,
		Active: func(context.Context) (Manifest, bool) {
			return h.active, h.active.ID != ""
		},
		Install: func(_ context.Context, version, backend string) (Job, error) {
			h.installed = append(h.installed, version+"/"+backend)
			return Job{ID: "b200-cpu"}, h.installEr
		},
		Wait: func(context.Context, string) (Job, error) {
			return Job{ID: "b200-cpu", State: h.jobState}, nil
		},
		Activate: func(_ context.Context, id string) error {
			h.activated = append(h.activated, id)
			return nil
		},
		InFlight: func() int { return int(h.inFlight.Load()) },
		IdlePoll: time.Millisecond,
	})
}

func newUpdateHarness(channel releases.Channel, tag string) *updateHarness {
	return &updateHarness{
		res:      &fakeResolver{stable: "b200", nightly: "b300"},
		active:   Manifest{ID: tag + "-cpu", Tag: tag, Channel: channel, Backend: "cpu"},
		jobState: StateDone,
	}
}

func TestNotifyComparesWithTheActiveChannel(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		channel releases.Channel
		tag     string
		want    bool
		latest  string
	}{
		{"old stable", releases.ChannelStable, "b100", true, "v1"},
		{"current stable", releases.ChannelStable, "b200", false, ""},
		{"stable ahead of the channel", releases.ChannelStable, "b250", false, ""},
		{"old nightly", releases.ChannelNightly, "b250", true, "b300"},
		{"current nightly", releases.ChannelNightly, "b300", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(tc.channel, tc.tag)
			c := h.checker(UpdatesNotify)
			c.Check(ctx)
			got := c.Info(ctx)
			if got.Available != tc.want || got.Latest != tc.latest || got.Stable != "v1" || got.Nightly != "b300" || got.CheckedAt.IsZero() {
				t.Errorf("%+v", got)
			}
			if len(h.installed) != 0 {
				t.Errorf("notify installed %v", h.installed)
			}
		})
	}
}

func TestInfoFollowsTheActiveBuild(t *testing.T) {
	ctx := context.Background()
	h := newUpdateHarness(releases.ChannelStable, "b100")
	c := h.checker(UpdatesNotify)
	c.Check(ctx)
	if !c.Info(ctx).Available {
		t.Fatal("want an update")
	}
	h.active.Tag = "b200"
	if c.Info(ctx).Available {
		t.Error("switching to the latest clears it")
	}
	h.active = Manifest{}
	if c.Info(ctx).Available {
		t.Error("no active build, nothing to update")
	}
}

func TestOffDoesNothing(t *testing.T) {
	h := newUpdateHarness(releases.ChannelStable, "b100")
	c := h.checker(UpdatesOff)
	c.Run(context.Background()) // returns at once
	if h.res.calls.Load() != 0 || c.Info(context.Background()).Available {
		t.Error("off must not look")
	}
}

func TestFailuresAreSoftAndKeepWhatWasFound(t *testing.T) {
	ctx := context.Background()
	h := newUpdateHarness(releases.ChannelStable, "b100")
	c := h.checker(UpdatesNotify)
	h.res.err = &releases.RateLimitError{Reset: time.Now().Add(time.Hour)}
	c.Check(ctx)
	got := c.Info(ctx)
	if got.Available || got.Error == "" {
		t.Errorf("%+v", got)
	}
	h.res.err = nil
	c.Check(ctx)
	h.res.err = errors.New("network")
	c.Check(ctx)
	got = c.Info(ctx)
	if !got.Available || got.Error == "" {
		t.Errorf("%+v", got)
	}
}

func TestAutoInstallsAndActivatesStable(t *testing.T) {
	h := newUpdateHarness(releases.ChannelStable, "b100")
	h.checker(UpdatesAuto).Check(context.Background())
	if len(h.installed) != 1 || h.installed[0] != "v1/cpu" || len(h.activated) != 1 || h.activated[0] != "b200-cpu" {
		t.Errorf("installed %v, activated %v", h.installed, h.activated)
	}
}

func TestAutoLeavesNightlyAlone(t *testing.T) {
	h := newUpdateHarness(releases.ChannelNightly, "b100")
	h.checker(UpdatesAuto).Check(context.Background())
	if len(h.installed) != 0 {
		t.Errorf("installed %v", h.installed)
	}
}

func TestAutoWaitsForIdle(t *testing.T) {
	h := newUpdateHarness(releases.ChannelStable, "b100")
	h.inFlight.Store(1)
	done := make(chan struct{})
	go func() {
		h.checker(UpdatesAuto).Check(context.Background())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("switched with a request in flight")
	case <-time.After(20 * time.Millisecond):
	}
	h.inFlight.Store(0)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("never switched")
	}
	if len(h.activated) != 1 {
		t.Errorf("activated %v", h.activated)
	}
}

func TestAutoGivesUpOnShutdownWhileBusy(t *testing.T) {
	h := newUpdateHarness(releases.ChannelStable, "b100")
	h.inFlight.Store(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.checker(UpdatesAuto).Check(ctx)
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
	if len(h.activated) != 0 {
		t.Errorf("activated %v", h.activated)
	}
}

func TestAutoFailures(t *testing.T) {
	h := newUpdateHarness(releases.ChannelStable, "b100")
	h.installEr = errors.New("no asset")
	h.checker(UpdatesAuto).Check(context.Background())
	if len(h.activated) != 0 {
		t.Error("activated after a failed install")
	}

	h = newUpdateHarness(releases.ChannelStable, "b100")
	h.jobState = StateFailed
	h.checker(UpdatesAuto).Check(context.Background())
	if len(h.activated) != 0 {
		t.Error("activated after a failed job")
	}

	h = newUpdateHarness(releases.ChannelStable, "b100")
	h.installEr = ErrAlreadyInstalled
	h.checker(UpdatesAuto).Check(context.Background())
	if len(h.activated) != 1 {
		t.Error("an installed build still needs activating")
	}
}

func TestRunChecksAtStartAndOnSchedule(t *testing.T) {
	h := newUpdateHarness(releases.ChannelStable, "b200")
	c := h.checker(UpdatesNotify)
	c.o.Interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for h.res.calls.Load() < 4 {
		if time.Now().After(deadline) {
			t.Fatal("no second check")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

func TestStoreFromKV(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	s := StoreFromKV(st)
	if id, err := s.ActiveBuild(ctx); id != "" || err != nil {
		t.Errorf("%q %v", id, err)
	}
	if err := s.SetActiveBuild(ctx, "b1-cpu"); err != nil {
		t.Fatal(err)
	}
	if id, err := s.ActiveBuild(ctx); id != "b1-cpu" || err != nil {
		t.Errorf("%q %v", id, err)
	}
}

func TestActiveManifest(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	m, err := New(Options{Dir: t.TempDir(), Releases: &fakeResolver{}, Hardware: func(context.Context) hardware.Snapshot { return hardware.Snapshot{} }, Store: StoreFromKV(st)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.ActiveManifest(ctx); ok {
		t.Error("none is active")
	}
	if err := st.SetKV(ctx, ActiveBuildKey, "b1-cpu"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.ActiveManifest(ctx); ok {
		t.Error("the active id is not on disk")
	}
}
