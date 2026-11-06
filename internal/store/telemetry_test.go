package store

import (
	"context"
	"testing"
	"time"
)

func TestTelemetryHistory(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	for _, sample := range []TelemetrySample{
		{At: base, Decode: 10, DecodePeak: 40, Prefill: 2, TTFT: 100, RPM: 6, KV: 0.5},
		{At: base.Add(10 * time.Second), Decode: 30, DecodePeak: 90, Prefill: 4, RPM: 2, KV: 0.1},
		{At: base.Add(2 * time.Minute), Decode: 7, DecodePeak: 7},
		{At: base.Add(time.Hour), Decode: 99, DecodePeak: 99},
	} {
		if err := s.InsertTelemetrySample(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.TelemetryHistory(ctx, base, base.Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("periods = %+v", got)
	}
	first := got[0]
	if !first.At.Equal(base) || first.Decode != 20 || first.DecodePeak != 90 || first.Prefill != 3 || first.RPM != 4 {
		t.Errorf("first = %+v", first)
	}
	if first.TTFT != 100 {
		t.Errorf("a quiet sample pulled TTFT to %v", first.TTFT)
	}
	if !got[1].At.Equal(base.Add(2*time.Minute)) || got[1].Decode != 7 || got[1].TTFT != 0 {
		t.Errorf("second = %+v", got[1])
	}

	if err := s.InsertTelemetrySample(ctx, TelemetrySample{At: base, Decode: 50, DecodePeak: 50}); err != nil {
		t.Fatal(err)
	}
	if got, err = s.TelemetryHistory(ctx, base, base.Add(5*time.Second), time.Second); err != nil || len(got) != 1 || got[0].Decode != 50 {
		t.Errorf("replacing: %+v %v", got, err)
	}
	if _, err = s.TelemetryHistory(ctx, base, base, 0); err == nil {
		t.Error("a zero step was accepted")
	}
}

func TestPruneTelemetry(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{base, base.Add(time.Hour)} {
		if err := s.InsertTelemetrySample(ctx, TelemetrySample{At: at}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Prune(ctx, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := s.TelemetryHistory(ctx, base, base.Add(2*time.Hour), time.Minute)
	if err != nil || len(got) != 1 || !got[0].At.Equal(base.Add(time.Hour)) {
		t.Errorf("after pruning: %+v %v", got, err)
	}
}
