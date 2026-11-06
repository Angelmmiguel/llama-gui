// Package config resolves the service's settings from flags and environment
// variables. Flags beat the environment, which beats the defaults.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrHelp is returned by Load when the arguments ask for the usage text.
var ErrHelp = flag.ErrHelp

// Config holds every setting documented in the README.
type Config struct {
	Addr         string
	Data         string
	Builds       string
	Models       string
	LlamaVersion string
	LlamaBackend string
	LlamaUpdates string
	KeepBuilds   int
	ModelsMax    int
	// MemoryMargin is in MiB.
	MemoryMargin int
	// LlamaArgs is the raw escape-hatch string, not yet split into arguments.
	LlamaArgs    string
	Capture      string
	Retention    time.Duration
	LogVerbosity int
	HFToken      string
	GitHubToken  string
	// GitHubAPI replaces api.github.com, for tests and the demo.
	GitHubAPI string
	// AllowedOrigins are the browser origins that may call inference routes, or
	// just "*". Lowercase, since that is how browsers send them.
	AllowedOrigins []string
	// AllowedHosts are extra host names to serve, or just "*". Lowercase.
	AllowedHosts []string

	// Version is the running binary's version. main sets it after Load; it is
	// not configurable.
	Version string
}

// Capture modes.
const (
	CaptureMeta   = "meta"
	CaptureBodies = "bodies"
)

var (
	backends = []string{"auto", "cpu", "cuda", "vulkan", "rocm", "sycl", "openvino"}
	updates  = []string{"off", "notify", "auto"}
	captures = []string{CaptureMeta, CaptureBodies}
)

var hostName = regexp.MustCompile(`^[a-z0-9_]([a-z0-9._-]*[a-z0-9_-])?$`)

// versionTag matches the tags llama.cpp publishes. It is deliberately narrow
// because a version later becomes part of a URL and a directory name.
var versionTag = regexp.MustCompile(`^(v[0-9]+(\.[0-9]+)*(-[0-9A-Za-z]+)*|b[0-9]+)$`)

// Load resolves the configuration from serve's arguments and an environment
// lookup. It returns ErrHelp for -h and --help, and otherwise an error naming
// the offending setting. Values are validated only after precedence is applied,
// so a valid flag rescues a bad environment variable.
func Load(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	str := func(name, key, fallback string) *string {
		if v := getenv(key); v != "" {
			fallback = v
		}
		return fs.String(name, fallback, key)
	}

	var cfg Config
	addr := str("addr", "LLAMA_GUI_ADDR", "127.0.0.1:8080")
	data := str("data", "LLAMA_GUI_DATA", "/data")
	builds := str("builds", "LLAMA_GUI_BUILDS", "/builds")
	models := str("models", "LLAMA_GUI_MODELS", "/models")
	version := str("llama-version", "LLAMA_GUI_LLAMA_VERSION", "stable")
	backend := str("llama-backend", "LLAMA_GUI_LLAMA_BACKEND", "auto")
	update := str("llama-updates", "LLAMA_GUI_LLAMA_UPDATES", "notify")
	keepBuilds := str("keep-builds", "LLAMA_GUI_KEEP_BUILDS", "3")
	modelsMax := str("models-max", "LLAMA_GUI_MODELS_MAX", "4")
	margin := str("memory-margin", "LLAMA_GUI_MEMORY_MARGIN", "2048")
	llamaArgs := str("llama-args", "LLAMA_GUI_LLAMA_ARGS", "")
	capture := str("capture", "LLAMA_GUI_CAPTURE", CaptureMeta)
	retention := str("retention", "LLAMA_GUI_RETENTION", "7d")
	verbosity := str("log-verbosity", "LLAMA_GUI_LOG_VERBOSITY", "3")
	hfToken := str("hf-token", "LLAMA_GUI_HF_TOKEN", getenv("HF_TOKEN"))
	githubToken := str("github-token", "LLAMA_GUI_GITHUB_TOKEN", "")
	githubAPI := str("github-api", "LLAMA_GUI_GITHUB_API", "")
	origins := str("allowed-origins", "LLAMA_GUI_ALLOWED_ORIGINS", "")
	hosts := str("allowed-hosts", "LLAMA_GUI_ALLOWED_HOSTS", "")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 {
		return cfg, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	cfg = Config{
		Addr:         *addr,
		Data:         *data,
		Builds:       *builds,
		Models:       *models,
		LlamaVersion: *version,
		LlamaBackend: *backend,
		LlamaUpdates: *update,
		LlamaArgs:    *llamaArgs,
		Capture:      *capture,
		HFToken:      *hfToken,
		GitHubToken:  *githubToken,
		GitHubAPI:    *githubAPI,
	}

	var err error
	if _, _, e := net.SplitHostPort(cfg.Addr); e != nil {
		err = errors.Join(err, fmt.Errorf("addr: %q is not host:port", cfg.Addr))
	}
	for name, dir := range map[string]string{"data": cfg.Data, "builds": cfg.Builds, "models": cfg.Models} {
		if dir == "" {
			err = errors.Join(err, fmt.Errorf("%s: must not be empty", name))
		}
	}
	if !isVersion(cfg.LlamaVersion) {
		err = errors.Join(err, fmt.Errorf("llama-version: %q is not stable, nightly, none or a tag such as v0.9.0 or b12132", cfg.LlamaVersion))
	}
	err = errors.Join(err,
		oneOf("llama-backend", cfg.LlamaBackend, backends),
		oneOf("llama-updates", cfg.LlamaUpdates, updates),
		oneOf("capture", cfg.Capture, captures),
	)
	for _, n := range []struct {
		name string
		raw  string
		min  int
		into *int
	}{
		{"keep-builds", *keepBuilds, 0, &cfg.KeepBuilds},
		{"models-max", *modelsMax, 1, &cfg.ModelsMax},
		{"memory-margin", *margin, 0, &cfg.MemoryMargin},
		{"log-verbosity", *verbosity, 0, &cfg.LogVerbosity},
	} {
		v, e := strconv.Atoi(n.raw)
		switch {
		case e != nil:
			err = errors.Join(err, fmt.Errorf("%s: %q is not a whole number", n.name, n.raw))
		case v < n.min:
			err = errors.Join(err, fmt.Errorf("%s: %d is below the minimum of %d", n.name, v, n.min))
		}
		*n.into = v
	}
	d, e := parseRetention(*retention)
	if e != nil {
		err = errors.Join(err, fmt.Errorf("retention: %w", e))
	}
	cfg.Retention = d
	if cfg.AllowedOrigins, e = parseList(*origins, isOrigin); e != nil {
		err = errors.Join(err, fmt.Errorf("allowed-origins: %w, such as https://chat.example.com", e))
	}
	if cfg.AllowedHosts, e = parseList(*hosts, hostName.MatchString); e != nil {
		err = errors.Join(err, fmt.Errorf("allowed-hosts: %w, a host name without scheme or port", e))
	}
	return cfg, err
}

// parseList reads a comma-separated list whose items pass valid, or a lone "*".
func parseList(raw string, valid func(string) bool) ([]string, error) {
	var out []string
	for item := range strings.SplitSeq(strings.ToLower(raw), ",") {
		item = strings.TrimSpace(item)
		switch {
		case item == "":
		case item == "*" || valid(item):
			out = append(out, item)
		default:
			return nil, fmt.Errorf("%q is not valid", item)
		}
	}
	if len(out) > 1 && slices.Contains(out, "*") {
		return nil, errors.New(`"*" cannot be combined with other values`)
	}
	return out, nil
}

// isOrigin says s is what a browser sends as Origin: scheme and host, nothing else.
func isOrigin(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		s == u.Scheme+"://"+u.Host
}

func isVersion(v string) bool {
	return v == "stable" || v == "nightly" || v == "none" || versionTag.MatchString(v)
}

func oneOf(name, v string, allowed []string) error {
	if slices.Contains(allowed, v) {
		return nil
	}
	return fmt.Errorf("%s: %q is not one of %s", name, v, strings.Join(allowed, ", "))
}

// parseRetention reads a positive duration, in days ("7d") or in anything
// time.ParseDuration accepts ("12h").
func parseRetention(s string) (time.Duration, error) {
	const day = 24 * time.Hour
	var d time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseInt(n, 10, 64)
		if err != nil || days > int64(1<<63-1)/int64(day) {
			return 0, fmt.Errorf("%q is not a duration such as 7d or 12h", s)
		}
		d = time.Duration(days) * day
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("%q is not a duration such as 7d or 12h", s)
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be longer than zero", s)
	}
	return d, nil
}
