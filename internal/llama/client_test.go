package llama

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", srv.Client())
}

func TestModels(t *testing.T) {
	for _, tc := range []struct {
		file, status string
		port         int
		effective    int
	}{
		{"models-loaded.json", "loaded", 54513, 4096},
		{"models-unloaded.json", "unloaded", 0, 0},
	} {
		t.Run(tc.file, func(t *testing.T) {
			c := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture(t, tc.file)) })
			ms, err := c.Models(context.Background())
			if err != nil || len(ms) != 1 {
				t.Fatalf("%v %v", ms, err)
			}
			m := ms[0]
			if m.ID != "smolvlm" || m.Status != tc.status || m.ContextLength != 8192 || m.Source != "preset" {
				t.Errorf("%+v", m)
			}
			wantPort := tc.port
			if tc.status == "unloaded" {
				wantPort = 0 // --port 0 in the captured args
			}
			if m.Port != wantPort || m.Memory.Effective.NCtx != tc.effective {
				t.Errorf("port %d effective %d", m.Port, m.Memory.Effective.NCtx)
			}
			d := m.Memory.Min.Devices
			if len(d) != 1 || d[0].Kind != "host" || d[0].Name != "Host" || d[0].Model != 173263104 || d[0].Context != 94371840 || d[0].Compute != 110120960 {
				t.Errorf("%+v", d)
			}
		})
	}
}

func TestSlotsQueryAndShape(t *testing.T) {
	var q map[string][]string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		w.Write(fixture(t, "slots-busy.json"))
	})
	s, err := c.Slots(context.Background(), "smolvlm")
	if err != nil || len(s) != 2 {
		t.Fatal(s, err)
	}
	if q["model"][0] != "smolvlm" || q["autoload"][0] != "false" {
		t.Errorf("query %v", q)
	}
	a := s[0]
	if !a.IsProcessing || a.TaskID != 42 || !a.HasTask || a.NCtx != 2048 || a.NDecoded != 133 || a.Params["top_k"] != float64(40) {
		t.Errorf("%+v", a)
	}
	if got := a.Tokens(); got != (Tokens{Cached: 3, Prompt: 15, Generated: 133, Total: 151}) {
		t.Errorf("%+v", got)
	}
	if s[1].HasTask || s[1].Tokens() != (Tokens{}) {
		t.Errorf("%+v", s[1])
	}
}

func TestSlotsIdleAfterCancel(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture(t, "slots-after-cancel.json")) })
	s, _ := c.Slots(context.Background(), "m")
	if !s[0].HasTask || s[0].TaskID != 42 || s[0].Tokens() != (Tokens{Cached: 307, Total: 307}) {
		t.Errorf("%+v %+v", s[0], s[0].Tokens())
	}
}

func TestParseMetrics(t *testing.T) {
	m := ParseMetrics(strings.NewReader(string(fixture(t, "metrics-busy.txt")) + "x{a=\"b\"} 1\nbad line\nnoval\n"))
	if m["prompt_tokens_total"] != 36 || m["tokens_predicted_seconds_total"] != 0.192477 || m["n_tokens_max"] != 304 {
		t.Errorf("%v", m)
	}
	if _, ok := m["x"]; ok {
		t.Error("labelled series kept")
	}
}

func TestMetricsAndProps(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("autoload") != "false" {
			t.Error("autoload")
		}
		switch r.URL.Path {
		case "/metrics":
			w.Write(fixture(t, "metrics-idle.txt"))
		case "/props":
			w.Write(fixture(t, "props.json"))
		}
	})
	m, err := c.Metrics(context.Background(), "x")
	if err != nil || len(m) == 0 {
		t.Fatal(m, err)
	}
	p, err := c.Props(context.Background(), "x")
	if err != nil || p["default_generation_settings"] == nil {
		t.Fatal(p, err)
	}
}

func TestErrUpstream(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"code":400,"message":"model 'nope' not found","type":"invalid_request_error"}}`))
	})
	err := c.Load(context.Background(), "nope")
	var ue *ErrUpstream
	if !errors.As(err, &ue) || ue.Status != 400 || ue.Message != "model 'nope' not found" {
		t.Fatalf("%v", err)
	}
	if errorMessage([]byte("boom")) != "boom" || errorMessage([]byte(`{"error":"x"}`)) != "x" {
		t.Error("fallbacks")
	}
}

func TestLoadUnloadSlotActionBodies(t *testing.T) {
	var got []string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+strings.TrimSpace(string(b)))
		w.Write(fixture(t, "slot-erase.txt")[:20]) // truncated JSON must not matter for Load
	})
	ctx := context.Background()
	_ = c.Load(ctx, "m")
	_ = c.Unload(ctx, "m")
	_, _ = c.SlotAction(ctx, "m", 1, "save", "s.bin")
	want := []string{
		`POST /models/load? {"model":"m"}`,
		`POST /models/unload? {"model":"m"}`,
		`POST /slots/1?action=save&autoload=false {"filename":"s.bin","model":"m"}`,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%q != %q", got[i], want[i])
		}
	}
}

func TestHealth(t *testing.T) {
	code := 200
	c := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	code = 503
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestTimings(t *testing.T) {
	tm, ok := ParseTimings(fixture(t, "nonstream.json"))
	if !ok || tm.CacheN != 4 || tm.PromptN != 7 || tm.PredictedN != 8 || tm.PredictedPerSecond < 155 {
		t.Errorf("%+v", tm)
	}
	if _, ok := ParseTimings([]byte(`{"a":1}`)); ok {
		t.Error("no timings")
	}
	if _, ok := ParseTimings([]byte(`{"timings":`)); ok {
		t.Error("broken json")
	}

	var found []Timings
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, "stream.txt"))))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if tm, ok := ScanSSELine(sc.Bytes()); ok {
			found = append(found, tm)
		}
	}
	if len(found) != 1 || found[0].PredictedN != 24 || found[0].PromptN != 14 {
		t.Errorf("%+v", found)
	}
}
