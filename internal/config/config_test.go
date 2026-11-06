package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Addr: "127.0.0.1:8080", Data: "/data", Builds: "/builds", Models: "/models",
		LlamaVersion: "stable", LlamaBackend: "auto", LlamaUpdates: "notify",
		KeepBuilds: 3, ModelsMax: 4, MemoryMargin: 2048, Capture: "meta",
		Retention: 7 * 24 * time.Hour, LogVerbosity: 3,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v\nwant %+v", cfg, want)
	}
}

// settings lists every README variable with its flag and a non-default value.
var settings = []struct {
	key, flag, value string
	get              func(Config) any
	want             any
}{
	{"LLAMA_GUI_ADDR", "addr", ":9090", func(c Config) any { return c.Addr }, ":9090"},
	{"LLAMA_GUI_DATA", "data", "/d", func(c Config) any { return c.Data }, "/d"},
	{"LLAMA_GUI_BUILDS", "builds", "/b", func(c Config) any { return c.Builds }, "/b"},
	{"LLAMA_GUI_MODELS", "models", "/m", func(c Config) any { return c.Models }, "/m"},
	{"LLAMA_GUI_LLAMA_VERSION", "llama-version", "b12132", func(c Config) any { return c.LlamaVersion }, "b12132"},
	{"LLAMA_GUI_LLAMA_BACKEND", "llama-backend", "vulkan", func(c Config) any { return c.LlamaBackend }, "vulkan"},
	{"LLAMA_GUI_LLAMA_UPDATES", "llama-updates", "auto", func(c Config) any { return c.LlamaUpdates }, "auto"},
	{"LLAMA_GUI_KEEP_BUILDS", "keep-builds", "5", func(c Config) any { return c.KeepBuilds }, 5},
	{"LLAMA_GUI_MODELS_MAX", "models-max", "2", func(c Config) any { return c.ModelsMax }, 2},
	{"LLAMA_GUI_MEMORY_MARGIN", "memory-margin", "512", func(c Config) any { return c.MemoryMargin }, 512},
	{"LLAMA_GUI_LLAMA_ARGS", "llama-args", "--foo bar", func(c Config) any { return c.LlamaArgs }, "--foo bar"},
	{"LLAMA_GUI_CAPTURE", "capture", "bodies", func(c Config) any { return c.Capture }, "bodies"},
	{"LLAMA_GUI_RETENTION", "retention", "12h", func(c Config) any { return c.Retention }, 12 * time.Hour},
	{"LLAMA_GUI_LOG_VERBOSITY", "log-verbosity", "4", func(c Config) any { return c.LogVerbosity }, 4},
	{"LLAMA_GUI_HF_TOKEN", "hf-token", "hf", func(c Config) any { return c.HFToken }, "hf"},
	{"LLAMA_GUI_GITHUB_TOKEN", "github-token", "gh", func(c Config) any { return c.GitHubToken }, "gh"},
	{"LLAMA_GUI_GITHUB_API", "github-api", "http://x", func(c Config) any { return c.GitHubAPI }, "http://x"},
	{"LLAMA_GUI_ALLOWED_ORIGINS", "allowed-origins", "https://chat.example.com", func(c Config) any { return c.AllowedOrigins }, []string{"https://chat.example.com"}},
	{"LLAMA_GUI_ALLOWED_HOSTS", "allowed-hosts", "llm.example.com", func(c Config) any { return c.AllowedHosts }, []string{"llm.example.com"}},
}

func TestEverySettingHasEnvAndFlag(t *testing.T) {
	for _, s := range settings {
		t.Run(s.key, func(t *testing.T) {
			cfg, err := Load(nil, env(map[string]string{s.key: s.value}))
			if err != nil {
				t.Fatal(err)
			}
			if got := s.get(cfg); !reflect.DeepEqual(got, s.want) {
				t.Errorf("env: got %v, want %v", got, s.want)
			}
			cfg, err = Load([]string{"--" + s.flag, s.value}, env(nil))
			if err != nil {
				t.Fatal(err)
			}
			if got := s.get(cfg); !reflect.DeepEqual(got, s.want) {
				t.Errorf("flag: got %v, want %v", got, s.want)
			}
		})
	}
}

func TestAllowedLists(t *testing.T) {
	origins := func(c Config) []string { return c.AllowedOrigins }
	hosts := func(c Config) []string { return c.AllowedHosts }
	for _, tc := range []struct {
		name, flag, value string
		get               func(Config) []string
		want              []string
		wantErr           bool
	}{
		{"several origins", "allowed-origins", "https://a.example, http://b.example:8080,", origins, []string{"https://a.example", "http://b.example:8080"}, false},
		{"origin is lowercased", "allowed-origins", "HTTPS://Chat.Example.com", origins, []string{"https://chat.example.com"}, false},
		{"any origin", "allowed-origins", "*", origins, []string{"*"}, false},
		{"origin with a path", "allowed-origins", "https://a.example/", origins, nil, true},
		{"origin with a query", "allowed-origins", "https://a.example?x=1", origins, nil, true},
		{"origin with credentials", "allowed-origins", "https://u@a.example", origins, nil, true},
		{"origin without a scheme", "allowed-origins", "a.example", origins, nil, true},
		{"other scheme", "allowed-origins", "ftp://a.example", origins, nil, true},
		{"star among origins", "allowed-origins", "*,https://a.example", origins, nil, true},
		{"several hosts", "allowed-hosts", "LLM.example.com, a.b", hosts, []string{"llm.example.com", "a.b"}, false},
		{"any host", "allowed-hosts", "*", hosts, []string{"*"}, false},
		{"host with a port", "allowed-hosts", "a.example:8080", hosts, nil, true},
		{"host with a scheme", "allowed-hosts", "https://a.example", hosts, nil, true},
		{"star among hosts", "allowed-hosts", "a.example,*", hosts, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load([]string{"--" + tc.flag, tc.value}, env(nil))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				if !strings.Contains(err.Error(), tc.flag) {
					t.Errorf("%v does not name %s", err, tc.flag)
				}
				return
			}
			if got := tc.get(cfg); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	cfg, err := Load([]string{"--addr", ":1"}, env(map[string]string{"LLAMA_GUI_ADDR": ":2"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":1" {
		t.Errorf("addr = %q", cfg.Addr)
	}
}

func TestValidFlagRescuesBadEnv(t *testing.T) {
	_, err := Load([]string{"--capture", "meta", "--keep-builds", "1"},
		env(map[string]string{"LLAMA_GUI_CAPTURE": "nope", "LLAMA_GUI_KEEP_BUILDS": "x"}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestHFTokenFallback(t *testing.T) {
	cfg, err := Load(nil, env(map[string]string{"HF_TOKEN": "plain"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HFToken != "plain" {
		t.Errorf("fallback: %q", cfg.HFToken)
	}
	cfg, err = Load(nil, env(map[string]string{"HF_TOKEN": "plain", "LLAMA_GUI_HF_TOKEN": "own"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HFToken != "own" {
		t.Errorf("precedence: %q", cfg.HFToken)
	}
}

func TestVersionsAccepted(t *testing.T) {
	for _, v := range []string{"stable", "nightly", "none", "v0.9.0", "v1.2", "v0.9.0-rc1", "b12132"} {
		if _, err := Load([]string{"--llama-version", v}, env(nil)); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
}

func TestRetention(t *testing.T) {
	for in, want := range map[string]time.Duration{"7d": 7 * 24 * time.Hour, "1d": 24 * time.Hour, "90m": 90 * time.Minute} {
		cfg, err := Load([]string{"--retention", in}, env(nil))
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if cfg.Retention != want {
			t.Errorf("%s = %v, want %v", in, cfg.Retention, want)
		}
	}
}

func TestInvalid(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--addr", "nope"}, "addr"},
		{[]string{"--data="}, "data"},
		{[]string{"--builds="}, "builds"},
		{[]string{"--models="}, "models"},
		{[]string{"--llama-version", "../../etc"}, "llama-version"},
		{[]string{"--llama-version", "latest"}, "llama-version"},
		{[]string{"--llama-backend", "cuda2"}, "llama-backend"},
		{[]string{"--llama-updates", "yes"}, "llama-updates"},
		{[]string{"--capture", "all"}, "capture"},
		{[]string{"--keep-builds", "-1"}, "keep-builds"},
		{[]string{"--models-max", "0"}, "models-max"},
		{[]string{"--memory-margin", "1G"}, "memory-margin"},
		{[]string{"--log-verbosity", "loud"}, "log-verbosity"},
		{[]string{"--retention", "week"}, "retention"},
		{[]string{"--retention", "0d"}, "retention"},
		{[]string{"--retention", "-1h"}, "retention"},
		{[]string{"--retention", "99999999999d"}, "retention"},
		{[]string{"stray"}, "stray"},
		{[]string{"--nope"}, "nope"},
	} {
		_, err := Load(tc.args, env(nil))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want it to mention %q", tc.args, err, tc.want)
		}
	}
}

func TestAllProblemsReportedTogether(t *testing.T) {
	_, err := Load([]string{"--capture", "x", "--models-max", "0", "--retention", "x"}, env(nil))
	for _, want := range []string{"capture", "models-max", "retention"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

func TestHelp(t *testing.T) {
	if _, err := Load([]string{"-h"}, env(nil)); !errors.Is(err, ErrHelp) {
		t.Errorf("err = %v", err)
	}
}
