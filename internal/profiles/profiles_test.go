package profiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/store"
)

func ptr[T any](v T) *T { return &v }

func valid() Profile {
	return Profile{Name: "Qwen", Alias: "qwen3-coder", ModelFile: "qwen/q.gguf", CtxSize: 4096}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Profile)
		bad  bool
	}{
		{"minimal", func(*Profile) {}, false},
		{"alias with slash", func(p *Profile) { p.Alias = "org/x" }, true},
		{"alias charset ok", func(p *Profile) { p.Alias = "Org.x_y:z-1" }, false},
		{"alias leading dash", func(p *Profile) { p.Alias = "-x" }, true},
		{"alias section injection", func(p *Profile) { p.Alias = "x]\n[y" }, true},
		{"alias star", func(p *Profile) { p.Alias = "*" }, true},
		{"no name", func(p *Profile) { p.Name = " " }, true},
		{"absolute model", func(p *Profile) { p.ModelFile = "/etc/passwd" }, true},
		{"traversal", func(p *Profile) { p.ModelFile = "a/../../b.gguf" }, true},
		{"inner dotdot", func(p *Profile) { p.ModelFile = "a/../b.gguf" }, true},
		{"mmproj traversal", func(p *Profile) { p.MMProj = "../m.gguf" }, true},
		{"mmproj ok", func(p *Profile) { p.MMProj = "qwen/m.gguf" }, false},
		{"backslash", func(p *Profile) { p.ModelFile = `a\b` }, true},
		{"newline in model", func(p *Profile) { p.ModelFile = "a\nb = c" }, true},
		{"ini meta in model", func(p *Profile) { p.ModelFile = "a;b.gguf" }, true},
		{"no ctx", func(p *Profile) { p.CtxSize = 0 }, true},
		{"ngl word", func(p *Profile) { p.NGL = "all" }, false},
		{"ngl junk", func(p *Profile) { p.NGL = "1\n[x]" }, true},
		{"split mode", func(p *Profile) { p.SplitMode = "diagonal" }, true},
		{"tensor split", func(p *Profile) { p.TensorSplit = "4,1.5" }, false},
		{"tensor split junk", func(p *Profile) { p.TensorSplit = "4;1" }, true},
		{"flash attn", func(p *Profile) { p.FlashAttn = "maybe" }, true},
		{"cache type", func(p *Profile) { p.CacheTypeK = "q8=0" }, true},
		{"negative temp", func(p *Profile) { p.Temp = ptr(-0.1) }, true},
		{"zero temp", func(p *Profile) { p.Temp = ptr(0.0) }, false},
		{"negative idle", func(p *Profile) { p.IdleUnloadSeconds = -1 }, true},
		{"extra ok", func(p *Profile) { p.ExtraArgs = []string{"--foo", "bar", "--flag"} }, false},
		{"extra port", func(p *Profile) { p.ExtraArgs = []string{"--port", "1"} }, true},
		{"extra host", func(p *Profile) { p.ExtraArgs = []string{"--host=0.0.0.0"} }, true},
		{"extra api key", func(p *Profile) { p.ExtraArgs = []string{"--api-key x"} }, true},
		{"extra models", func(p *Profile) { p.ExtraArgs = []string{"--models-max", "1"} }, true},
		{"extra no-models", func(p *Profile) { p.ExtraArgs = []string{"--no-models-autoload"} }, true},
		{"extra model bypass", func(p *Profile) { p.ExtraArgs = []string{"--model", "/etc/passwd"} }, true},
		{"extra short", func(p *Profile) { p.ExtraArgs = []string{"-m", "x"} }, true},
		{"extra stray value", func(p *Profile) { p.ExtraArgs = []string{"bar"} }, true},
		{"extra meta", func(p *Profile) { p.ExtraArgs = []string{"--foo", "a#b"} }, true},
		{"extra newline", func(p *Profile) { p.ExtraArgs = []string{"--foo=a\x01b"} }, true},
		{"extra repeats field", func(p *Profile) { p.ExtraArgs = []string{"--ctx-size", "1"} }, true},
		{"extra repeated", func(p *Profile) { p.ExtraArgs = []string{"--foo", "--foo"} }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := valid()
			c.mut(&p)
			err := p.Validate()
			if c.bad != (err != nil) {
				t.Fatalf("Validate() = %v, want bad=%v", err, c.bad)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("not ErrInvalid: %v", err)
			}
		})
	}
}

func fullProfile() Profile {
	return Profile{
		Name: "Full", Alias: "full", ModelFile: "d/m.gguf", MMProj: "d/mm.gguf", Jinja: ptr(true), MLock: true,
		CtxSize: 65536, Parallel: 4, NGL: "48", SplitMode: "layer", TensorSplit: "4,1", FlashAttn: "on",
		CacheTypeK: "q8_0", CacheTypeV: "q8_0", CacheReuse: ptr(256), Batch: 2048, UBatch: 512, Threads: 16,
		Temp: ptr(0.7), TopP: ptr(0.8), TopK: ptr(0), MinP: ptr(0.0), RepeatPenalty: ptr(1.05), LoadAtStart: true,
		ExtraArgs: []string{"--foo bar", "--flag"},
	}
}

func TestRenderPreset(t *testing.T) {
	got, skipped := RenderPreset([]Profile{fullProfile(), valid()}, "/models", nil)
	want := `version = 1

[full]
model = /models/d/m.gguf
mmproj = /models/d/mm.gguf
ctx-size = 65536
parallel = 4
n-gpu-layers = 48
split-mode = layer
tensor-split = 4,1
flash-attn = on
cache-type-k = q8_0
cache-type-v = q8_0
cache-reuse = 256
batch-size = 2048
ubatch-size = 512
threads = 16
temp = 0.7
top-p = 0.8
top-k = 0
min-p = 0
repeat-penalty = 1.05
jinja = 1
mlock = 1
load-on-startup = 1
foo = bar
flag = 1

[qwen3-coder]
model = /models/qwen/q.gguf
ctx-size = 4096

`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if skipped != nil {
		t.Fatalf("skipped %v", skipped)
	}
}

func TestRenderPresetSkips(t *testing.T) {
	p := fullProfile()
	got, skipped := RenderPreset([]Profile{p}, "/m", func(f string) bool { return f != "mlock" && f != "load-mode" && f != "foo" && f != "load-on-startup" })
	if strings.Contains(got, "mlock") || strings.Contains(got, "foo") {
		t.Fatalf("not filtered:\n%s", got)
	}
	if !strings.Contains(got, "load-on-startup = 1") || !strings.Contains(got, "model = /m/d/m.gguf") {
		t.Fatalf("preset keys must not be filtered:\n%s", got)
	}
	if len(skipped) != 2 || skipped[0].Flag != "mlock" || skipped[1].Flag != "foo" {
		t.Fatalf("skipped = %+v", skipped)
	}

	bad := valid()
	bad.ModelFile = "../evil.gguf"
	got, skipped = RenderPreset([]Profile{bad, valid()}, "/m", nil)
	if strings.Contains(got, "evil") || !strings.Contains(got, "[qwen3-coder]") {
		t.Fatalf("invalid profile rendered:\n%s", got)
	}
	if len(skipped) != 1 || skipped[0].Flag != "" {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestRenderNoJinja(t *testing.T) {
	p := valid()
	p.Jinja = ptr(false)
	got, _ := RenderPreset([]Profile{p}, "/m", nil)
	if !strings.Contains(got, "no-jinja = 1\n") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestWritePresetAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "preset.ini")
	for _, content := range []string{"one", "two"} {
		if err := WritePreset(path, content); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("read %q, %v", got, err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("leftovers: %v %v", entries, err)
	}
}

func TestCommand(t *testing.T) {
	p := fullProfile()
	want := "llama-server \\\n" +
		"  -m /models/d/m.gguf --mmproj /models/d/mm.gguf --alias full \\\n" +
		"  -c 65536 -np 4 -ngl 48 -fa on \\\n" +
		"  -ctk q8_0 -ctv q8_0 -sm layer -ts 4,1 --cache-reuse 256 -b 2048 -ub 512 -t 16 --temp 0.7 --top-p 0.8 --top-k 0 --min-p 0 --repeat-penalty 1.05 --jinja --mlock --foo bar --flag"
	if got := Command(p, "/models", nil); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	bare := valid()
	bare.ModelFile = "my model.gguf"
	want = "llama-server \\\n  -m '/m/my model.gguf' --alias qwen3-coder \\\n  -c 4096"
	if got := Command(bare, "/m", nil); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	preset := filepath.Join(dir, "preset.ini")
	return NewService(st, "/models", preset), preset
}

func TestService(t *testing.T) {
	ctx := context.Background()
	s, preset := newService(t)

	bad := valid()
	bad.CtxSize = 0
	if _, err := s.Create(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create invalid: %v", err)
	}

	p := valid()
	p.IdleUnloadSeconds, p.OnDemand, p.Temp = 30, true, ptr(0.0)
	created, err := s.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("no id")
	}
	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Temp == nil || *got.Temp != 0 || got.Alias != p.Alias {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	data, err := os.ReadFile(preset)
	if err != nil || !strings.Contains(string(data), "[qwen3-coder]") || !strings.Contains(string(data), "temp = 0\n") {
		t.Fatalf("preset %q, %v", data, err)
	}
	if !s.Allow(p.Alias) || s.Allow("other") {
		t.Fatal("Allow")
	}
	if d, ok := s.IdlePolicy(p.Alias); !ok || d != 30*time.Second {
		t.Fatalf("IdlePolicy = %v %v", d, ok)
	}

	if _, err := s.Create(ctx, valid()); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate alias: %v", err)
	}

	got.Alias = "renamed"
	if _, err := s.Update(ctx, got.ID, got); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(preset); strings.Contains(string(data), "qwen3-coder") || !strings.Contains(string(data), "[renamed]") {
		t.Fatalf("preset not updated: %s", data)
	}
	if _, err := s.Update(ctx, "missing", valid()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}

	if err := s.Delete(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, got.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	if data, _ := os.ReadFile(preset); string(data) != "version = 1\n\n" {
		t.Fatalf("preset after delete: %q", data)
	}
}

func TestServiceSyncReportsSkipped(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	p := valid()
	p.MLock = true
	if _, err := s.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	s.SetSupported(func(f string) bool { return f != "mlock" && f != "load-mode" })
	skipped, err := s.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].Flag != "mlock" || len(s.Skipped()) != 1 {
		t.Fatalf("skipped = %+v", skipped)
	}
}

func TestReservedRejectsFileAndRemoteFlags(t *testing.T) {
	for _, key := range []string{"log-file", "path", "lora", "chat-template-file", "model-url", "hf-repo", "hf", "media-path", "models-dir", "slot-save-path", "api-key-file", "ssl-key-file"} {
		if !reserved(key) {
			t.Errorf("reserved(%q) = false", key)
		}
	}
	for _, key := range []string{"keep", "no-mmap", "rope-scaling", "ctx-shift"} {
		if reserved(key) {
			t.Errorf("reserved(%q) = true", key)
		}
	}
}

func TestReservedRejectsWhatReachesBeyondTheModel(t *testing.T) {
	for _, key := range []string{
		"tools", "tools-runtime", "agent", "ag", "no-agent", "no-ag", "mcp-servers-config", "mcp-servers-json",
		"ui-mcp-proxy", "webui-mcp-proxy", "no-ui-mcp-proxy", "rpc", "connect", "connect-code",
		"spec-draft-model", "model-draft", "md", "spec-draft-hf", "hf-repo-draft", "hfd", "hfrd", "docker-repo", "dr",
		"m", "mm", "mu", "mmu", "hfr", "hff", "hft", "a", "lookup-cache-static", "lookup-cache-dynamic", "lcs", "lcd", "jf",
		"embd-gemma-default", "gpt-oss-20b-default", "fim-qwen-7b-spec", "video-ffmpeg-dir",
	} {
		if !reserved(key) {
			t.Errorf("reserved(%q) = false", key)
		}
		p := valid()
		p.ExtraArgs = []string{"--" + key + " x"}
		if err := p.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("--%s passed validation: %v", key, err)
		}
	}
}

func TestSpeculativeDecodingArgsPass(t *testing.T) {
	p := valid()
	p.ExtraArgs = []string{"--spec-type draft-mtp --spec-draft-n-max 2 --spec-draft-ngl 99 --spec-default"}
	if err := p.Validate(); err != nil {
		t.Error(err)
	}
}

// Short names are aliases at the router, so a flag is only reserved if all its names are.
func TestReservedCoversEveryNameOfAFlag(t *testing.T) {
	raw, err := os.ReadFile("../llama/testdata/help.txt")
	if err != nil {
		t.Fatal(err)
	}
	spacing := regexp.MustCompile(`,\s+`)
	name := regexp.MustCompile(`(?:^|\s)--?([a-z0-9][a-z0-9.-]*)`)
	checked := 0
	for line := range strings.Lines(string(raw)) {
		if !strings.HasPrefix(line, "-") {
			continue
		}
		head, _, _ := strings.Cut(spacing.ReplaceAllString(strings.TrimRight(line, "\n"), ", "), "  ")
		var names []string
		for _, m := range name.FindAllStringSubmatch(strings.ReplaceAll(head, ",", " "), -1) {
			names = append(names, m[1])
		}
		if !slices.ContainsFunc(names, reserved) {
			continue
		}
		checked++
		for _, n := range names {
			if !reserved(n) {
				t.Errorf("--%s is not reserved, but another name of it is: %v", n, names)
			}
		}
	}
	if checked < 30 {
		t.Errorf("only %d reserved flags found in the help: is its format still understood?", checked)
	}
}

func TestMLockFollowsTheBuild(t *testing.T) {
	p := Profile{Name: "m", Alias: "m", ModelFile: "m.gguf", CtxSize: 4096, MLock: true}
	for name, tc := range map[string]struct {
		flags                  []string
		preset, command, avoid string
	}{
		"newer build": {[]string{"ctx-size", "load-mode"}, "load-mode = mmap+mlock\n", "--load-mode mmap+mlock", "mlock = 1"},
		"older build": {[]string{"ctx-size", "mlock"}, "mlock = 1\n", "--mlock", "load-mode"},
		"both":        {[]string{"ctx-size", "mlock", "load-mode"}, "mlock = 1\n", "--mlock", "load-mode"},
	} {
		t.Run(name, func(t *testing.T) {
			supported := func(f string) bool { return slices.Contains(tc.flags, f) }
			got, skipped := RenderPreset([]Profile{p}, "/m", supported)
			if !strings.Contains(got, tc.preset) || strings.Contains(got, tc.avoid) || len(skipped) != 0 {
				t.Errorf("preset:\n%s\nskipped: %+v", got, skipped)
			}
			if cmd := Command(p, "/m", supported); !strings.Contains(cmd, tc.command) || strings.Contains(cmd, tc.avoid) {
				t.Errorf("command: %s", cmd)
			}
		})
	}
	if _, skipped := RenderPreset([]Profile{p}, "/m", func(f string) bool { return f == "ctx-size" }); len(skipped) != 1 || skipped[0].Flag != "mlock" {
		t.Errorf("a build with neither flag: skipped = %+v", skipped)
	}
}
