// Command llama-gui is a dashboard and control plane for llama.cpp: a single
// binary that installs llama-server, supervises it, and fronts it as the only
// entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/app"
	"github.com/Angelmmiguel/llama-gui/internal/config"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const healthcheckTimeout = 3 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], getenv, stdout, stderr)
	case "version":
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	case "healthcheck":
		return healthcheck(args[1:], getenv, stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "llama-gui: unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `llama-gui is a dashboard and control plane for llama.cpp.

usage:
  llama-gui serve [flags]        run the service
  llama-gui healthcheck [flags]  exit 0 if the local service is healthy, else 1
  llama-gui version              print the version

not available yet: doctor, status, logs, llama, models

flags, each with an environment variable that it overrides:
  --addr           LLAMA_GUI_ADDR           listen address (default 127.0.0.1:8080)
  --data           LLAMA_GUI_DATA           database, profiles and slot saves (default /data)
  --builds         LLAMA_GUI_BUILDS         downloaded llama.cpp binaries (default /builds)
  --models         LLAMA_GUI_MODELS         GGUF directory (default /models)
  --llama-version  LLAMA_GUI_LLAMA_VERSION  version to install on first start: stable, nightly,
                                            a tag such as v0.9.0 or b12132, or none (default stable)
  --llama-backend  LLAMA_GUI_LLAMA_BACKEND  auto, cpu, cuda, vulkan, rocm, sycl or openvino (default auto)
  --llama-updates  LLAMA_GUI_LLAMA_UPDATES  off, notify or auto (default notify)
  --keep-builds    LLAMA_GUI_KEEP_BUILDS    builds to keep for rollback (default 3)
  --models-max     LLAMA_GUI_MODELS_MAX     models loaded at once (default 4)
  --memory-margin  LLAMA_GUI_MEMORY_MARGIN  MiB kept free per GPU (default 2048)
  --llama-args     LLAMA_GUI_LLAMA_ARGS     extra arguments for every llama-server
  --capture        LLAMA_GUI_CAPTURE        meta or bodies (default meta)
  --retention      LLAMA_GUI_RETENTION      how long history and logs are kept (default 7d)
  --log-verbosity  LLAMA_GUI_LOG_VERBOSITY  llama-server -lv level (default 3)
  --hf-token       LLAMA_GUI_HF_TOKEN       Hugging Face token (HF_TOKEN also works)
  --github-token   LLAMA_GUI_GITHUB_TOKEN   raises GitHub's rate limit
`)
}

func serve(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := config.Load(args, getenv)
	if errors.Is(err, config.ErrHelp) {
		usage(stdout)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "llama-gui: %v\n", err)
		return 2
	}
	cfg.Version = version

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, log); err != nil {
		log.Error("exiting", "error", err)
		return 1
	}
	return 0
}

func healthcheck(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := config.Load(args, getenv)
	if errors.Is(err, config.ErrHelp) {
		usage(stdout)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "llama-gui: %v\n", err)
		return 1
	}
	url, err := healthURL(cfg.Addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "llama-gui: %v\n", err)
		return 1
	}
	// A bare transport, because a proxy from the environment must not answer
	// for the local service.
	client := http.Client{Timeout: healthcheckTimeout, Transport: &http.Transport{}}
	resp, err := client.Get(url)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "llama-gui: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "llama-gui: healthz answered %s\n", resp.Status)
		return 1
	}
	return 0
}

// healthURL turns a listen address into one to dial: a wildcard host such as
// the image's ":8080" means this machine.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("addr: %w", err)
	}
	if ip, err := netip.ParseAddr(host); host == "" || (err == nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/v1/healthz", nil
}
