// Package app is the composition root: it builds every component, wires them,
// runs them and stops them.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Angelmmiguel/llama-gui/internal/api"
	"github.com/Angelmmiguel/llama-gui/internal/app/reaper"
	"github.com/Angelmmiguel/llama-gui/internal/config"
	"github.com/Angelmmiguel/llama-gui/internal/store"
	"github.com/Angelmmiguel/llama-gui/web"
)

const (
	dbFile = "llama-gui.db"
	// shutdownTimeout leaves room inside Docker's default 10 s stop timeout.
	shutdownTimeout = 5 * time.Second
	pruneInterval   = time.Hour
)

// Run serves until ctx ends or the listener fails.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if os.Getpid() == 1 {
		go reaper.Reap(ctx, log)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return run(ctx, cfg, log, ln)
}

// run is Run over an existing listener, which it takes ownership of.
func run(ctx context.Context, cfg config.Config, log *slog.Logger, ln net.Listener) error {
	defer func() { _ = ln.Close() }()

	if err := os.MkdirAll(cfg.Data, 0o750); err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	st, err := store.Open(filepath.Join(cfg.Data, dbFile))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	bgCtx, stopBackground := context.WithCancel(ctx)
	var background sync.WaitGroup
	defer background.Wait()
	defer stopBackground()
	background.Go(func() { pruneLoop(bgCtx, st, cfg.Retention, pruneInterval, log) })

	handler := routes(api.Handler(api.Deps{
		Version: cfg.Version,
		Capture: cfg.Capture,
		Started: time.Now(),
		Now:     time.Now,
		State:   func() string { return "stopped" },
	}), web.Handler(), proxyStub())

	if !isLoopback(ln.Addr()) {
		log.Warn("listening beyond loopback with no authentication; anyone who can reach this port can use the API, "+
			"so keep it on a trusted network or behind an authenticating reverse proxy", "addr", ln.Addr().String())
	}
	log.Info("listening", "addr", ln.Addr().String(), "data", cfg.Data, "version", cfg.Version)
	return serve(ctx, ln, handler, shutdownTimeout, log)
}

// serve runs the HTTP server until ctx ends, then drains it for at most drain.
func serve(ctx context.Context, ln net.Listener, handler http.Handler, drain time.Duration, log *slog.Logger) error {
	srv := &http.Server{
		Handler: handler,
		// There is deliberately no WriteTimeout: inference responses and the
		// SSE streams stay open for as long as they need.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down")
	drainCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		// Long streams are normal, so running out of patience is no failure.
		log.Warn("closing connections that did not drain", "error", err)
		if err := srv.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("close: %w", err)
		}
	}
	return nil
}

// pruneLoop applies retention at startup and then every interval.
func pruneLoop(ctx context.Context, st *store.Store, retention, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		n, err := st.Prune(ctx, time.Now().Add(-retention))
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("pruning history", "error", err)
		case n > 0:
			log.Info("pruned history", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func isLoopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}
