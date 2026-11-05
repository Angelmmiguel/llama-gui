package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func cli(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, noEnv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := cli(t, "version")
	if code != 0 || strings.TrimSpace(out) != version {
		t.Errorf("%d %q", code, out)
	}
}

func TestUsageAndExitCodes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"nope"}, 2},
		{[]string{"status", "extra"}, 2},
		{[]string{"logs", "extra"}, 2},
		{[]string{"logs", "--nope"}, 2},
		{[]string{"logs", "-n", "0"}, 2},
		{[]string{"llama"}, 2},
		{[]string{"llama", "explode"}, 2},
		{[]string{"llama", "use"}, 2},
		{[]string{"llama", "install"}, 2},
		{[]string{"models"}, 2},
		{[]string{"models", "explode"}, 2},
		{[]string{"models", "load"}, 2},
		{[]string{"models", "pull"}, 2},
		{[]string{"models", "rm"}, 2},
		{[]string{"help"}, 0},
		{[]string{"--help"}, 0},
		{[]string{"serve", "--help"}, 0},
		{[]string{"healthcheck", "-h"}, 0},
	} {
		code, out, errOut := cli(t, tc.args...)
		if code != tc.code {
			t.Errorf("%v: exit %d, want %d", tc.args, code, tc.code)
		}
		if !strings.Contains(out+errOut, "usage:") {
			t.Errorf("%v: no usage in %q", tc.args, out+errOut)
		}
	}
}

func TestUsageNamesEverySubcommand(t *testing.T) {
	_, out, _ := cli(t, "help")
	for _, name := range []string{"serve", "version", "healthcheck", "doctor", "status", "logs", "llama", "models"} {
		if !strings.Contains(out, name) {
			t.Errorf("usage does not mention %s", name)
		}
	}
}

func TestServeRejectsBadConfig(t *testing.T) {
	code, _, errOut := cli(t, "serve", "--capture", "nope")
	if code != 2 || !strings.Contains(errOut, "capture") {
		t.Errorf("%d %q", code, errOut)
	}
}

// healthServer answers healthz on a wildcard listener and returns its port.
func healthServer(t *testing.T, status int) string {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/healthz" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(status)
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func TestHealthcheck(t *testing.T) {
	port := healthServer(t, http.StatusOK)
	for _, addr := range []string{":" + port, "0.0.0.0:" + port, "[::]:" + port, "127.0.0.1:" + port, "localhost:" + port} {
		if code, _, errOut := cli(t, "healthcheck", "--addr", addr); code != 0 {
			t.Errorf("%s: exit %d: %s", addr, code, errOut)
		}
	}
}

func TestHealthcheckReadsEnv(t *testing.T) {
	port := healthServer(t, http.StatusOK)
	env := func(k string) string {
		if k == "LLAMA_GUI_ADDR" {
			return ":" + port
		}
		return ""
	}
	if code := run([]string{"healthcheck"}, env, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Errorf("exit %d", code)
	}
}

func TestHealthcheckFailures(t *testing.T) {
	unhealthy := healthServer(t, http.StatusInternalServerError)
	if code, _, _ := cli(t, "healthcheck", "--addr", ":"+unhealthy); code != 1 {
		t.Errorf("unhealthy: exit %d", code)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	if code, _, _ := cli(t, "healthcheck", "--addr", addr); code != 1 {
		t.Errorf("nothing listening: exit %d", code)
	}

	if code, _, _ := cli(t, "healthcheck", "--capture", "nope"); code != 1 {
		t.Errorf("bad config: exit %d", code)
	}
}
