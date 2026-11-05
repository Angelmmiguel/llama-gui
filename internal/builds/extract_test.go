package builds

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func extractEntries(t *testing.T, maxBytes int64, entries ...entry) (string, error) {
	t.Helper()
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "a.tar.gz")
	if err := os.WriteFile(archive, tgz(t, entries...), 0o600); err != nil {
		t.Fatal(err)
	}
	return dest, extract(context.Background(), archive, dest, []string{"top"}, maxBytes)
}

func TestExtractRejects(t *testing.T) {
	sym := func(name, link string) entry { return entry{name: name, typeflag: tar.TypeSymlink, link: link} }
	dir := func(name string) entry { return entry{name: name, typeflag: tar.TypeDir, mode: 0o755} }
	for name, entries := range map[string][]entry{
		"absolute path":          {{name: "/tmp/evil", body: "x"}},
		"traversal":              {{name: "top/../../evil", body: "x"}},
		"traversal staying in":   {{name: "top/a/../b", body: "x"}},
		"only dots":              {{name: "..", typeflag: tar.TypeDir, mode: 0o755}},
		"outside top":            {{name: "other/file", body: "x"}},
		"file as top":            {{name: "top", body: "x"}},
		"absolute symlink":       {sym("top/l", "/etc/passwd")},
		"escaping symlink":       {sym("top/l", "../evil")},
		"deep escaping symlink":  {dir("top/a/"), sym("top/a/l", "../../evil")},
		"dotdot after component": {dir("top/a/"), sym("top/l", "a/../../evil")},
		"symlink chain":          {dir("top/a/"), sym("top/s", "a"), sym("top/up", "s/../.."), {name: "top/up/evil", body: "x"}},
		"write through symlink":  {dir("top/a/"), sym("top/s", "a"), {name: "top/s/evil", body: "x"}},
		"link under a symlink":   {sym("top/s", "."), sym("top/s/x", "../evil")},
		"dir through symlink":    {dir("top/a/"), sym("top/s", "a"), dir("top/s/b/")},
		"empty symlink":          {sym("top/l", "")},
		"escaping hard link":     {{name: "top/h", typeflag: tar.TypeLink, link: "../../etc/passwd"}},
		"absolute hard link":     {{name: "top/h", typeflag: tar.TypeLink, link: "/etc/passwd"}},
		"hard link outside top":  {{name: "top/h", typeflag: tar.TypeLink, link: "other/file"}},
		"hard link to symlink":   {sym("top/l", "x"), {name: "top/h", typeflag: tar.TypeLink, link: "top/l"}},
		"hard link to nothing":   {{name: "top/h", typeflag: tar.TypeLink, link: "top/none"}},
		"hard link to itself":    {{name: "top/f", body: "x"}, {name: "top/f", typeflag: tar.TypeLink, link: "top/f"}},
		"char device":            {{name: "top/dev", typeflag: tar.TypeChar}},
		"block device":           {{name: "top/dev", typeflag: tar.TypeBlock}},
		"fifo":                   {{name: "top/fifo", typeflag: tar.TypeFifo}},
		"file over a directory":  {dir("top/a/"), {name: "top/a/x", body: "x"}, {name: "top/a", body: "y"}},
	} {
		t.Run(name, func(t *testing.T) {
			dest, err := extractEntries(t, 1<<20, entries...)
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Errorf("err = %v", err)
			}
			if _, statErr := os.Lstat(filepath.Join(filepath.Dir(dest), "evil")); statErr == nil {
				t.Error("wrote outside the destination")
			}
		})
	}
}

func TestExtractAccepts(t *testing.T) {
	dest, err := extractEntries(t, 1<<20,
		entry{name: "top/", typeflag: tar.TypeDir, mode: 0o700},
		entry{name: "./top/dotted", body: "x"},
		entry{name: "top/bin/run", body: "#!", mode: 0o4777},
		entry{name: "top/lib.so.1", body: "lib"},
		entry{name: "top/lib.so", typeflag: tar.TypeSymlink, link: "lib.so.1"},
		entry{name: "top/bin/up", typeflag: tar.TypeSymlink, link: "../lib.so.1"},
		entry{name: "top/hard", typeflag: tar.TypeLink, link: "top/lib.so.1"},
		entry{name: "top/dup", body: "first"},
		entry{name: "top/dup", body: "second"},
		entry{name: "top/dead", typeflag: tar.TypeSymlink, link: "missing"},
		entry{name: "top/dead", body: "not a link any more"},
	)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, "bin/run"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode(); perm.Perm() != 0o755 || perm&os.ModeSetuid != 0 {
		t.Errorf("mode = %v", perm)
	}
	for file, want := range map[string]string{"bin/up": "lib", "hard": "lib", "lib.so": "lib", "dup": "second", "dotted": "x", "dead": "not a link any more"} {
		got, err := os.ReadFile(filepath.Join(dest, file))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", file, got, err)
		}
	}
}

func TestExtractSizeBound(t *testing.T) {
	big := strings.Repeat("x", 600)
	if _, err := extractEntries(t, 1000, entry{name: "top/a", body: big}, entry{name: "top/b", body: big}); !errors.Is(err, ErrUnsafeArchive) {
		t.Errorf("err = %v", err)
	}
	if _, err := extractEntries(t, 1000, entry{name: "top/a", body: big}); err != nil {
		t.Errorf("under the limit: %v", err)
	}
}

func TestExtractGarbage(t *testing.T) {
	base := t.TempDir()
	archive := filepath.Join(base, "a.tar.gz")
	if err := os.WriteFile(archive, []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extract(context.Background(), archive, base, []string{"top"}, 1<<20); !errors.Is(err, ErrUnsafeArchive) {
		t.Errorf("err = %v", err)
	}
}

func TestExtractCanceled(t *testing.T) {
	base := t.TempDir()
	archive := filepath.Join(base, "a.tar.gz")
	if err := os.WriteFile(archive, tgz(t, entry{name: "top/a", body: "x"}), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := extract(ctx, archive, base, []string{"top"}, 1<<20); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
