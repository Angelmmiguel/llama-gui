package logs

import (
	"bufio"
	"os"
	"testing"
	"time"
)

var t0 = time.Unix(1000, 0)

func model(p int) string {
	if p == 54513 {
		return "smolvlm"
	}
	return ""
}

func TestParseTable(t *testing.T) {
	cases := []struct {
		name, raw string
		want      Line
	}{
		{"router srv", `{"type":"log","time":20195,"level":"info","msg":"srv  server_run_r: starting server in router mode.\n"}`,
			Line{Level: "info", Source: "srv", Message: "srv  server_run_r: starting server in router mode."}},
		{"router instance", `{"type":"log","time":1,"level":"info","msg":"srv           run: instance name=smolvlm exited with status 0\n"}`,
			Line{Level: "info", Source: "router", Message: "srv           run: instance name=smolvlm exited with status 0"}},
		{"router warn", `{"type":"log","time":1,"level":"warn","msg":"srv  server_setup: security: x\n"}`,
			Line{Level: "warn", Source: "srv", Message: "srv  server_setup: security: x"}},
		{"nested child", `{"type":"log","time":1,"level":"none","msg":"[54513] {\"type\":\"log\",\"time\":9018,\"level\":\"warn\",\"msg\":\"srv  server_setup: x\\n\"}\n"}`,
			Line{Level: "warn", Source: "srv", Port: 54513, Model: "smolvlm", Message: "srv  server_setup: x"}},
		{"child ggml", `{"type":"log","time":1,"level":"none","msg":"[54513] {\"type\":\"log\",\"time\":9018,\"level\":\"warn\",\"msg\":\"load_tensors: ffn up/down are swapped\\n\"}\n"}`,
			Line{Level: "warn", Source: "ggml", Port: 54513, Model: "smolvlm", Message: "load_tensors: ffn up/down are swapped"}},
		{"child slot", `{"type":"log","time":1,"level":"none","msg":"[54513] {\"type\":\"log\",\"time\":1,\"level\":\"info\",\"msg\":\"slot launch_slot_: id  1 | task 33 | processing task\\n\"}\n"}`,
			Line{Level: "info", Source: "slot", Port: 54513, Model: "smolvlm", Message: "slot launch_slot_: id  1 | task 33 | processing task", Slot: 1, HasSlot: true, Task: 33, HasTask: true}},
		{"slot task -1", `{"type":"log","time":1,"level":"none","msg":"[54513] {\"type\":\"log\",\"time\":1,\"level\":\"info\",\"msg\":\"slot get_availabl: id  0 | task -1 | selected slot\\n\"}\n"}`,
			Line{Level: "info", Source: "slot", Port: 54513, Model: "smolvlm", Message: "slot get_availabl: id  0 | task -1 | selected slot", Slot: 0, HasSlot: true}},
		{"measure child", `{"type":"log","time":1,"level":"none","msg":"[    0] {\"type\":\"log\",\"time\":9,\"level\":\"info\",\"msg\":\"srv  llama_server: initializing ...\\n\"}\n"}`,
			Line{Level: "info", Source: "srv", Message: "srv  llama_server: initializing ..."}},
		{"non-log child", `{"type":"log","time":1,"level":"none","msg":"[54513] {\"type\":\"fit_memory_breakdown\",\"data\":{}}\n"}`,
			Line{Level: "debug", Source: "other", Port: 54513, Model: "smolvlm", Message: `{"type":"fit_memory_breakdown","data":{}}`}},
		{"non-log top", `{"type":"fit_memory_breakdown","data":{}}`,
			Line{Level: "debug", Source: "other", Message: `{"type":"fit_memory_breakdown","data":{}}`}},
		{"plain prefixed port", "[54513] W srv  foo: bar",
			Line{Level: "warn", Source: "srv", Port: 54513, Model: "smolvlm", Message: "srv  foo: bar"}},
		{"plain error", "E slot x: id  2 | task 5 | boom",
			Line{Level: "error", Source: "slot", Message: "slot x: id  2 | task 5 | boom", Slot: 2, HasSlot: true, Task: 5, HasTask: true}},
		{"timestamp prefix", "0.00.123.456 I llama_model_loader: loaded",
			Line{Level: "info", Source: "ggml", Message: "llama_model_loader: loaded"}},
		{"ansi", "\x1b[32mI\x1b[0m srv  a: b", Line{Level: "info", Source: "srv", Message: "srv  a: b"}},
		{"ansi plain", "\x1b[1msrv  a: b\x1b[0m", Line{Level: "info", Source: "srv", Message: "srv  a: b"}},
		{"garbage", "}{ not json", Line{Level: "info", Source: "other", Message: "}{ not json"}},
		{"broken json", `{"type":"log","msg":`, Line{Level: "info", Source: "other", Message: `{"type":"log","msg":`}},
		{"empty", "", Line{Level: "info", Source: "other"}},
		{"cmn tag", `{"type":"log","time":1,"level":"info","msg":"cmn  common_param: x\n"}`, Line{Level: "info", Source: "cmn", Message: "cmn  common_param: x"}},
		{"print_info", "print_info: arch = llama", Line{Level: "info", Source: "ggml", Message: "print_info: arch = llama"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parser{}.Parse(tc.raw, t0, model)
			tc.want.Raw, tc.want.Time = tc.raw, t0
			if got != tc.want {
				t.Errorf("\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseRouterLogFile(t *testing.T) {
	f, err := os.Open("../llama/testdata/router-jsonl.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	var events []string
	var n int
	d := Detector{}
	for sc.Scan() {
		l := Parser{}.Parse(sc.Text(), t0, model)
		n++
		if l.Message == "" && l.Raw != "" {
			t.Errorf("empty message for %q", l.Raw)
		}
		if l.Port == 54513 && l.Model != "smolvlm" {
			t.Errorf("model not mapped: %+v", l)
		}
		for _, e := range d.Observe(l) {
			events = append(events, e.Kind+" "+e.Severity)
		}
	}
	if n < 90 {
		t.Fatalf("only %d lines", n)
	}
	want := map[string]bool{"router.started info": false, "router.listening info": false, "model.loading info": false, "model.loaded info": false, "instance.exited info": false}
	for _, e := range events {
		delete(want, e)
	}
	if len(want) > 0 {
		t.Errorf("missing events %v in %v", want, events)
	}
}

func TestDetector(t *testing.T) {
	p := Parser{}
	obs := func(raw string) []Event { return Detector{}.Observe(p.Parse(raw, t0, model)) }
	if e := obs(`{"type":"log","time":1,"level":"info","msg":"srv           run: instance name=a exited with status 1\n"}`); len(e) != 1 || e[0].Severity != "error" || e[0].Meta["status"] != "1" {
		t.Errorf("%+v", e)
	}
	if e := obs(`{"type":"log","time":1,"level":"info","msg":"srv          load: spawning server instance with name=a on port 5\n"}`); len(e) != 1 || e[0].Kind != EventModelLoading || e[0].Meta["port"] != "5" {
		t.Errorf("%+v", e)
	}
	if e := obs(`{"type":"log","time":1,"level":"info","msg":"srv  server_on_li: listening on http://127.0.0.1:18080\n"}`); len(e) != 1 || e[0].Meta["addr"] != "http://127.0.0.1:18080" {
		t.Errorf("%+v", e)
	}
	if e := obs("[54513] srv  server_run_i: model loaded"); len(e) != 1 || e[0].Kind != EventModelLoaded || e[0].Meta["model"] != "smolvlm" {
		t.Errorf("%+v", e)
	}
	if e := obs("[54513] E srv send_error: request exceeds the available context size"); len(e) != 1 || e[0].Kind != EventContextExceeded {
		t.Errorf("%+v", e)
	}
	if e := obs("srv  x: queue is full"); len(e) != 1 || e[0].Kind != EventQueueFull {
		t.Errorf("%+v", e)
	}
	if e := obs("slot x: deferring task"); len(e) != 1 || e[0].Kind != EventDeferred {
		t.Errorf("%+v", e)
	}
	for _, raw := range []string{"", "garbage", `{"type":"log","level":"info","msg":"srv  proxy_reques: proxying request\n"}`} {
		if e := obs(raw); len(e) != 0 {
			t.Errorf("%q: %+v", raw, e)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"", "{", "[", "[1] ", "[99999999999999999999] x", `{"type":"log","level":"none","msg":"[1] {"}`,
		`{"type":"log","level":"none","msg":"[ 1] {\"type\":\"log\"}"}`, "\x1b[", "I ", "slot x: id  | task",
		`{"type":"log","time":3501,"level":"info","msg":"srv  llama_server: initializing ...\n"}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		l := Parser{}.Parse(s, t0, model)
		Detector{}.Observe(l)
		if l.Raw != s {
			t.Fatal("raw altered")
		}
	})
}
