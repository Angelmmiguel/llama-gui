package builds

import (
	"os"
	"slices"
	"testing"
)

func TestParseRealCaptures(t *testing.T) {
	version, err := os.ReadFile("../llama/testdata/version.txt")
	if err != nil {
		t.Fatal(err)
	}
	p := parseVersion(version)
	if p.Version != "0.9.0-dev" || p.Build != "12132" || p.Commit != "2ada83079" || p.VersionOutput == "" {
		t.Errorf("version = %+v", p)
	}

	help, err := os.ReadFile("../llama/testdata/help.txt")
	if err != nil {
		t.Fatal(err)
	}
	flags := parseFlags(help)
	for _, want := range []string{"--models-preset", "-h", "--help", "--usage", "--threads", "-t", "-fa", "--flash-attn", "-kvo", "--no-kv-offload", "--models-max", "--log-jsonl"} {
		if !slices.Contains(flags, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, f := range flags {
		if !flagRE.MatchString(f) {
			t.Errorf("bad flag %q", f)
		}
	}

	devices, err := os.ReadFile("../llama/testdata/list-devices.txt")
	if err != nil {
		t.Fatal(err)
	}
	if d := parseDevices(devices); len(d) != 0 {
		t.Errorf("devices = %q", d)
	}
}

func TestParseVersionTolerance(t *testing.T) {
	if p := parseVersion([]byte("loading...\nversion: 7213 (abc123)\n")); p.Version != "7213" || p.Build != "7213" || p.Commit != "abc123" {
		t.Errorf("old = %+v", p)
	}
	if p := parseVersion([]byte("version: 2.0 something new\n")); p.Version != "2.0" || p.Build != "" {
		t.Errorf("new = %+v", p)
	}
	if p := parseVersion([]byte("no idea")); p.Version != "" || p.VersionOutput != "no idea" {
		t.Errorf("unknown = %+v", p)
	}
}

func TestParseFlagsIgnoresContinuationLines(t *testing.T) {
	help := "-a,    --alpha N    text\n                     (see --not-a-flag)\n                     --also-not\nprose --nope\n"
	if got := parseFlags([]byte(help)); !slices.Equal(got, []string{"--alpha", "-a"}) {
		t.Errorf("flags = %q", got)
	}
}
