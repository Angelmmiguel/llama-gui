package fakellama

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	//go:embed assets/version.txt
	versionTxt string
	//go:embed assets/help.txt
	helpTxt string
	//go:embed assets/list-devices.txt
	devicesTxt string
)

// Run is the whole program: it returns the process exit code. Cancelling ctx is SIGTERM.
func Run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opts, mode := ParseArgs(args)
	switch mode {
	case ModeVersion:
		fmt.Fprint(stdout, versionTxt)
		return 0
	case ModeHelp:
		fmt.Fprint(stdout, helpTxt)
		return 0
	case ModeListDevices:
		fmt.Fprint(stdout, devicesTxt)
		return 0
	}
	opts.ApplyEnv(getenv)
	s, err := New(opts, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "fake-llama-server:", err)
		return 1
	}
	ln, err := s.Listen()
	if err != nil {
		fmt.Fprintln(stderr, "fake-llama-server:", err)
		return 1
	}
	s.Banner(ln.Addr().String())

	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	var crash <-chan time.Time
	if opts.CrashAfter > 0 {
		crash = time.After(opts.CrashAfter)
	}
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if srv.Shutdown(shut) != nil {
			_ = srv.Close()
		}
		return 0
	case <-crash:
		_ = srv.Close()
		return 1
	case err := <-errc:
		fmt.Fprintln(stderr, "fake-llama-server:", strings.TrimSpace(err.Error()))
		return 1
	}
}
