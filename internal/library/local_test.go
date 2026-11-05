package library

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "Single-Q4_K_M.gguf"), sampleGGUF(3))
	write(t, filepath.Join(root, "mmproj-stray.gguf"), []byte("x"))
	write(t, filepath.Join(root, "notes.txt"), []byte("x"))
	write(t, filepath.Join(root, "vision", "model-Q8_0.gguf"), []byte("12345"))
	write(t, filepath.Join(root, "vision", "mmproj-model-f16.gguf"), []byte("123"))
	write(t, filepath.Join(root, "big", "big-Q6_K-00002-of-00002.gguf"), []byte("22"))
	write(t, filepath.Join(root, "big", "big-Q6_K-00001-of-00002.gguf"), []byte("1"))
	write(t, filepath.Join(root, "big", "x.gguf.partial"), []byte("1"))
	write(t, filepath.Join(root, "empty", "readme.md"), []byte("x"))

	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	big, single, vision := got[0], got[1], got[2]
	if big.ID != "big" || big.Shards != 2 || big.SizeBytes != 3 || big.Quant != "Q6_K" || big.Name != "big" {
		t.Errorf("big: %+v", big)
	}
	if single.ID != "Single-Q4_K_M" || single.Quant != "Q4_K_M" || single.Name != "Test Model" || single.Metadata == nil || single.Metadata.BlockCount != 48 {
		t.Errorf("single: %+v", single)
	}
	if vision.MMProj != "mmproj-model-f16.gguf" || vision.SizeBytes != 8 || vision.Quant != "Q8_0" || len(vision.Files) != 2 {
		t.Errorf("vision: %+v", vision)
	}
	if m, err := Scan(filepath.Join(root, "missing")); err != nil || m != nil {
		t.Errorf("missing dir: %v %v", m, err)
	}
}

func TestDelete(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.gguf")
	write(t, outside, []byte("x"))
	write(t, filepath.Join(root, "a.gguf"), []byte("x"))
	write(t, filepath.Join(root, "b.txt"), []byte("x"))
	write(t, filepath.Join(root, "dir", "m.gguf"), []byte("x"))
	write(t, filepath.Join(root, "dir", "mmproj.gguf"), []byte("x"))
	write(t, filepath.Join(root, "mixed", "m.gguf"), []byte("x"))
	write(t, filepath.Join(root, "mixed", "keep.txt"), []byte("x"))
	if err := os.Symlink(outside, filepath.Join(root, "link.gguf")); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"", ".", "..", "../outside.gguf", "/etc/passwd", "a/b/c.gguf", "link.gguf", "dir/../../outside.gguf"} {
		if err := Delete(root, rel); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Delete(%q) = %v, want ErrInvalidPath", rel, err)
		}
	}
	for _, rel := range []string{"b.txt", "mixed"} {
		if err := Delete(root, rel); !errors.Is(err, ErrNotModel) {
			t.Errorf("Delete(%q) = %v, want ErrNotModel", rel, err)
		}
	}
	if err := Delete(root, "missing.gguf"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	for _, rel := range []string{"a.gguf", "dir", "dir2"} {
		write(t, filepath.Join(root, "dir2", "z.gguf"), []byte("x"))
		if err := Delete(root, rel); err != nil {
			t.Errorf("Delete(%q) = %v", rel, err)
		}
		if _, err := os.Lstat(filepath.Join(root, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%q still exists", rel)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("outside file was touched")
	}
}

func TestDiskUsage(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.gguf"), make([]byte, 100))
	write(t, filepath.Join(root, "d", "b.gguf"), make([]byte, 50))
	write(t, filepath.Join(root, "c.txt"), make([]byte, 999))
	d, err := DiskUsage(root)
	if err != nil {
		t.Fatal(err)
	}
	if d.GGUFBytes != 150 || d.TotalBytes <= 0 || d.FreeBytes <= 0 || d.FreeBytes > d.TotalBytes {
		t.Errorf("%+v", d)
	}
}

func TestQuantFromName(t *testing.T) {
	for name, want := range map[string]string{
		"Qwen3-32B-Q4_K_M.gguf": "Q4_K_M", "gpt-oss-20b-mxfp4.gguf": "MXFP4", "x-f16.gguf": "F16",
		"mmproj-SmolVLM-256M-Instruct-Q8_0.gguf": "Q8_0", "m-IQ4_XS.gguf": "IQ4_XS", "m-BF16-00001-of-00002.gguf": "BF16",
		"m-UD-Q4_K_XL.gguf": "UD-Q4_K_XL", "Llama-3.1-8B.gguf": "", "m.Q5_K_S.gguf": "Q5_K_S", "m-Q4_K_M-00001-of-00003.gguf": "Q4_K_M",
	} {
		if got := quantFromName(name); got != want {
			t.Errorf("%s: got %q want %q", name, got, want)
		}
	}
}
