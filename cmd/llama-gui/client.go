package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

const (
	clientTimeout = 30 * time.Second
	maxSSELine    = 16 << 20
)

// jobPoll is how often install checks on its job.
var jobPoll = time.Second

// apiClient is a thin client of the local API.
type apiClient struct {
	base string
	http *http.Client
}

func newClient(addr string) (*apiClient, error) {
	base, err := baseURL(addr)
	if err != nil {
		return nil, err
	}
	// A bare transport, because a proxy from the environment must not answer for
	// the local service.
	return &apiClient{base: base + "/api/v1", http: &http.Client{Transport: &http.Transport{}}}, nil
}

// do sends a request and decodes the JSON answer into out, if not nil.
func (c *apiClient) do(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, clientTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("is the service running? %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := apiError(resp); err != nil {
		return err
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func apiError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e) == nil && e.Error != "" {
		return errors.New(e.Error)
	}
	return fmt.Errorf("the service answered %s", resp.Status)
}

// clientCommand parses --addr and the command's flags, wherever they are among
// the arguments, and returns the client and the positional arguments. The code is
// non-zero, and has been reported, when the command should not go on.
func clientCommand(name string, args []string, getenv func(string) string, stderr io.Writer, define func(*flag.FlagSet)) (*apiClient, []string, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	addr := "127.0.0.1:8080"
	if v := getenv("LLAMA_GUI_ADDR"); v != "" {
		addr = v
	}
	fs.StringVar(&addr, "addr", addr, "address of the local service")
	if define != nil {
		define(fs)
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			_, _ = fmt.Fprintf(stderr, "llama-gui %s: %v\n\n", name, err)
			usage(stderr)
			return nil, nil, 2
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	client, err := newClient(addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "llama-gui: %v\n", err)
		return nil, nil, 1
	}
	return client, positional, 0
}

// finish reports an error from a command and returns its exit code.
func finish(stderr io.Writer, err error) int {
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "llama-gui: %v\n", err)
	return 1
}

func usageError(stderr io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(stderr, "llama-gui: "+format+"\n\n", args...)
	usage(stderr)
	return 2
}

func status(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	c, rest, code := clientCommand("status", args, getenv, stderr, nil)
	if code != 0 {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "status takes no arguments")
	}
	return finish(stderr, c.printStatus(context.Background(), stdout))
}

func (c *apiClient) printStatus(ctx context.Context, w io.Writer) error {
	var st struct {
		State           string `json:"state"`
		Version         string `json:"version"`
		PID             int    `json:"pid"`
		UptimeSeconds   int64  `json:"uptime_seconds"`
		Capture         string `json:"capture"`
		LastError       string `json:"last_error"`
		ModelsLoaded    int    `json:"models_loaded"`
		UpdateAvailable bool   `json:"update_available"`
		Update          struct {
			Latest string `json:"latest"`
		} `json:"update"`
		ActiveBuild *struct {
			ID string `json:"id"`
		} `json:"active_build"`
	}
	if err := c.do(ctx, http.MethodGet, "/status", nil, &st); err != nil {
		return err
	}
	var slots struct {
		Models []struct {
			ID    string `json:"id"`
			Slots []struct {
				ID    int     `json:"id"`
				State string  `json:"state"`
				Total int     `json:"total"`
				NCtx  int     `json:"n_ctx"`
				Speed float64 `json:"speed"`
			} `json:"slots"`
		} `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, "/slots", nil, &slots); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	line := func(k, format string, args ...any) {
		_, _ = fmt.Fprintf(tw, "%s\t"+format+"\n", append([]any{k}, args...)...)
	}
	state := st.State
	if st.PID != 0 {
		state += fmt.Sprintf(" (pid %d)", st.PID)
	}
	line("server", "%s", state)
	if st.LastError != "" {
		line("last error", "%s", st.LastError)
	}
	line("version", "%s, up %s, capture %s", st.Version, time.Duration(st.UptimeSeconds)*time.Second, st.Capture)
	if st.ActiveBuild != nil {
		line("build", "%s", st.ActiveBuild.ID)
	} else {
		line("build", "none")
	}
	if st.UpdateAvailable {
		line("update", "%s available", st.Update.Latest)
	}
	line("models", "%d loaded", st.ModelsLoaded)
	for _, m := range slots.Models {
		for _, s := range m.Slots {
			line("slot", "%s/%d %s, %d/%d tokens, %.1f tok/s", m.ID, s.ID, s.State, s.Total, s.NCtx, s.Speed)
		}
	}
	return tw.Flush()
}

type logLine struct {
	TS      time.Time `json:"ts"`
	Level   string    `json:"level"`
	Model   string    `json:"model"`
	Message string    `json:"message"`
	Raw     string    `json:"raw"`
}

func (l logLine) String() string {
	text := l.Message
	if text == "" {
		text = l.Raw
	}
	model := ""
	if l.Model != "" {
		model = " [" + l.Model + "]"
	}
	return fmt.Sprintf("%s %-5s%s %s", l.TS.Local().Format("15:04:05.000"), l.Level, model, text)
}

func logsCommand(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	var follow bool
	var n int
	c, rest, code := clientCommand("logs", args, getenv, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&follow, "f", false, "keep following")
		fs.IntVar(&n, "n", 100, "lines of backlog")
	})
	if code != 0 {
		return code
	}
	if len(rest) > 0 || n < 1 || n > 1000 {
		return usageError(stderr, "logs takes -f and -n from 1 to 1000")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if follow {
		if err = c.followLogs(ctx, n, stdout); ctx.Err() != nil {
			err = nil
		}
	} else {
		err = c.printLogs(ctx, n, stdout)
	}
	return finish(stderr, err)
}

func (c *apiClient) printLogs(ctx context.Context, n int, w io.Writer) error {
	var out struct {
		Lines []logLine `json:"lines"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/logs?limit=%d", n), nil, &out); err != nil {
		return err
	}
	slices.Reverse(out.Lines)
	for _, l := range out.Lines {
		_, _ = fmt.Fprintln(w, l)
	}
	return nil
}

func (c *apiClient) followLogs(ctx context.Context, n int, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/logs/stream?backlog=%d", c.base, n), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("is the service running? %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := apiError(resp); err != nil {
		return err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, maxSSELine)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			event = v
		} else if v, ok := strings.CutPrefix(line, "data: "); ok && event == "log" {
			var l logLine
			if err := json.Unmarshal([]byte(v), &l); err == nil {
				_, _ = fmt.Fprintln(w, l)
			}
		}
	}
	return sc.Err()
}

type buildInfo struct {
	ID          string    `json:"id"`
	Tag         string    `json:"tag"`
	ReleaseTag  string    `json:"release_tag"`
	Channel     string    `json:"channel"`
	Backend     string    `json:"backend"`
	InstalledAt time.Time `json:"installed_at"`
	Active      bool      `json:"active"`
	Supported   bool      `json:"supported"`
}

type jobInfo struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Step  string `json:"step"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Error string `json:"error"`
}

func llamaCommand(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageError(stderr, "llama needs a subcommand")
	}
	sub, args := args[0], args[1:]
	var channel, backend string
	c, rest, code := clientCommand("llama "+sub, args, getenv, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&channel, "channel", "", "stable or nightly")
		fs.StringVar(&backend, "backend", "auto", "backend to install")
	})
	if code != 0 {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch sub {
	case "list":
		return finish(stderr, c.listBuilds(ctx, stdout))
	case "available":
		return finish(stderr, c.availableBuilds(ctx, channel, stdout))
	case "install", "use", "remove":
		if len(rest) != 1 {
			return usageError(stderr, "llama %s needs a version", sub)
		}
		switch sub {
		case "install":
			return finish(stderr, c.install(ctx, rest[0], backend, stdout))
		case "use":
			return finish(stderr, c.useBuild(ctx, rest[0], stdout))
		}
		return finish(stderr, c.removeBuild(ctx, rest[0], stdout))
	}
	return usageError(stderr, "unknown llama subcommand %q", sub)
}

func (c *apiClient) builds(ctx context.Context) ([]buildInfo, error) {
	var out struct {
		Builds []buildInfo `json:"builds"`
	}
	err := c.do(ctx, http.MethodGet, "/builds", nil, &out)
	return out.Builds, err
}

func (c *apiClient) listBuilds(ctx context.Context, w io.Writer) error {
	list, err := c.builds(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tVERSION\tCHANNEL\tBACKEND\tINSTALLED\t")
	for _, b := range list {
		mark := ""
		if b.Active {
			mark = "active"
		} else if !b.Supported {
			mark = "unsupported"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", b.ID, b.ReleaseTag, b.Channel, b.Backend, b.InstalledAt.Local().Format(time.DateOnly), mark)
	}
	return tw.Flush()
}

func (c *apiClient) availableBuilds(ctx context.Context, channel string, w io.Writer) error {
	var out struct {
		Releases []struct {
			Tag         string    `json:"tag"`
			Channel     string    `json:"channel"`
			PublishedAt time.Time `json:"published_at"`
			Options     []struct {
				Backend string `json:"backend"`
				Reason  string `json:"reason"`
			} `json:"options"`
		} `json:"releases"`
	}
	path := "/builds/available"
	if channel != "" {
		path += "?channel=" + url.QueryEscape(channel)
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "VERSION\tCHANNEL\tPUBLISHED\tBACKENDS")
	for _, r := range out.Releases {
		var backends []string
		for _, o := range r.Options {
			if o.Reason == "" {
				backends = append(backends, o.Backend)
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Tag, r.Channel, r.PublishedAt.Local().Format(time.DateOnly), strings.Join(backends, ","))
	}
	return tw.Flush()
}

func (c *apiClient) install(ctx context.Context, version, backend string, w io.Writer) error {
	var job jobInfo
	body := map[string]string{"version": version, "backend": backend}
	if err := c.do(ctx, http.MethodPost, "/builds/install", body, &job); err != nil {
		return err
	}
	last := ""
	for {
		var out struct {
			Jobs []jobInfo `json:"jobs"`
		}
		if err := c.do(ctx, http.MethodGet, "/builds/jobs", nil, &out); err != nil {
			return err
		}
		i := slices.IndexFunc(out.Jobs, func(j jobInfo) bool { return j.ID == job.ID })
		if i < 0 {
			// Nothing was started: the build was already on disk.
			_, _ = fmt.Fprintf(w, "%s is already installed\n", job.ID)
			return nil
		}
		j := out.Jobs[i]
		switch j.State {
		case "done":
			_, _ = fmt.Fprintf(w, "installed %s\n", j.ID)
			return nil
		case "failed", "canceled":
			return fmt.Errorf("install of %s %s: %s", j.ID, j.State, j.Error)
		}
		progress := j.State
		if j.Total > 0 {
			progress = fmt.Sprintf("%s %d%%", j.State, j.Done*100/j.Total)
		}
		if progress != last {
			_, _ = fmt.Fprintf(w, "%s: %s\n", j.ID, progress)
			last = progress
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jobPoll):
		}
	}
}

// findBuild matches a build by id or version. A version installed for several
// backends has to be named by id.
func (c *apiClient) findBuild(ctx context.Context, version string) (buildInfo, error) {
	list, err := c.builds(ctx)
	if err != nil {
		return buildInfo{}, err
	}
	var matches []buildInfo
	for _, b := range list {
		if b.ID == version {
			return b, nil
		}
		if b.Tag == version || b.ReleaseTag == version {
			matches = append(matches, b)
		}
	}
	switch len(matches) {
	case 0:
		return buildInfo{}, fmt.Errorf("%s is not installed", version)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, b := range matches {
		ids[i] = b.ID
	}
	return buildInfo{}, fmt.Errorf("%s matches several builds, name one: %s", version, strings.Join(ids, ", "))
}

func (c *apiClient) useBuild(ctx context.Context, version string, w io.Writer) error {
	b, err := c.findBuild(ctx, version)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPost, "/builds/"+url.PathEscape(b.ID)+"/activate", nil, nil); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "switched to %s, the router is restarting\n", b.ID)
	return nil
}

func (c *apiClient) removeBuild(ctx context.Context, version string, w io.Writer) error {
	b, err := c.findBuild(ctx, version)
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodDelete, "/builds/"+url.PathEscape(b.ID), nil, nil); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "removed %s\n", b.ID)
	return nil
}

func modelsCommand(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageError(stderr, "models needs a subcommand")
	}
	sub, args := args[0], args[1:]
	c, rest, code := clientCommand("models "+sub, args, getenv, stderr, nil)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	switch sub {
	case "list":
		return finish(stderr, c.listModels(ctx, stdout))
	case "load", "unload":
		if len(rest) != 1 {
			return usageError(stderr, "models %s needs a model id", sub)
		}
		err := c.do(ctx, http.MethodPost, "/models/"+url.PathEscape(rest[0])+"/"+sub, nil, nil)
		if err == nil {
			_, _ = fmt.Fprintf(stdout, "%s: %s requested\n", rest[0], sub)
		}
		return finish(stderr, err)
	case "pull", "rm":
		return usageError(stderr, "models %s arrives with the model library (M7)", sub)
	}
	return usageError(stderr, "unknown models subcommand %q", sub)
}

func (c *apiClient) listModels(ctx context.Context, w io.Writer) error {
	var out struct {
		Models []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, "/models", nil, &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTATUS")
	for _, m := range out.Models {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", m.ID, m.Status)
	}
	return tw.Flush()
}
