package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Errors from Delete.
var (
	ErrInvalidPath = errors.New("invalid model path")
	ErrNotModel    = errors.New("not a GGUF file or a directory of GGUF files")
)

const localMetaLimit = 64 << 20

// LocalModel is one model as the router sees it: a file at the root, or a directory.
type LocalModel struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"` // relative to the models directory
	SizeBytes int64     `json:"size_bytes"`
	Quant     string    `json:"quant"`
	Files     []string  `json:"files"`
	MMProj    string    `json:"mmproj,omitempty"`
	Shards    int       `json:"shards"`
	Metadata  *Metadata `json:"metadata,omitempty"`
	ModTime   time.Time `json:"mod_time"`
}

func isGGUF(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".gguf") }

// Scan lists the models under modelsDir in the router's layout. A missing directory is empty.
func Scan(modelsDir string) ([]LocalModel, error) {
	entries, err := os.ReadDir(modelsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []LocalModel
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var m *LocalModel
		switch {
		case e.Type().IsRegular() && isGGUF(e.Name()) && !isMMProjName(e.Name()):
			m, err = scanFile(modelsDir, e.Name())
		case e.IsDir():
			m, err = scanDir(modelsDir, e.Name())
		}
		if err != nil {
			return nil, err
		}
		if m != nil {
			out = append(out, *m)
		}
	}
	slices.SortFunc(out, func(a, b LocalModel) int { return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID)) })
	return out, nil
}

func readMeta(path string) *Metadata {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	m, err := ParseGGUF(f, localMetaLimit)
	if err != nil {
		return nil
	}
	return &m
}

func finish(m *LocalModel, mainFile string) {
	if m.Metadata != nil {
		m.Quant = m.Metadata.Quant
		m.Name = m.Metadata.Name
	}
	if m.Quant == "" {
		m.Quant = quantFromName(mainFile)
	}
	if m.Name == "" {
		m.Name = m.ID
	}
}

func scanFile(root, name string) (*LocalModel, error) {
	info, err := os.Stat(filepath.Join(root, name))
	if err != nil {
		return nil, err
	}
	m := &LocalModel{
		ID: strings.TrimSuffix(name, filepath.Ext(name)), Path: name, SizeBytes: info.Size(),
		Files: []string{name}, Shards: 1, ModTime: info.ModTime(),
		Metadata: readMeta(filepath.Join(root, name)),
	}
	finish(m, name)
	return m, nil
}

func scanDir(root, name string) (*LocalModel, error) {
	entries, err := os.ReadDir(filepath.Join(root, name))
	if err != nil {
		return nil, err
	}
	m := &LocalModel{ID: name, Path: name, Shards: 1}
	var mainFile string
	for _, e := range entries {
		if !e.Type().IsRegular() || !isGGUF(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, e.Name())
		m.SizeBytes += info.Size()
		if info.ModTime().After(m.ModTime) {
			m.ModTime = info.ModTime()
		}
		switch idx, total, sharded := shardInfo(e.Name()); {
		case isMMProjName(e.Name()):
			if m.MMProj == "" || e.Name() < m.MMProj {
				m.MMProj = e.Name()
			}
		case sharded:
			m.Shards = max(m.Shards, total)
			if idx == 1 {
				mainFile = e.Name()
			}
		case mainFile == "" || e.Name() < mainFile:
			mainFile = e.Name()
		}
	}
	if mainFile == "" {
		return nil, nil
	}
	m.Metadata = readMeta(filepath.Join(root, name, mainFile))
	finish(m, mainFile)
	return m, nil
}

// Delete removes the model at relPath: a .gguf file, or a directory holding only .gguf files.
// Symlinks are never followed.
func Delete(modelsDir, relPath string) error {
	rel := filepath.Clean(relPath)
	if relPath == "" || filepath.IsAbs(rel) || rel == "." || !filepath.IsLocal(rel) {
		return fmt.Errorf("%w: %q", ErrInvalidPath, relPath)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) > 2 {
		return fmt.Errorf("%w: %q", ErrInvalidPath, relPath)
	}
	cur := modelsDir
	var info fs.FileInfo
	for _, p := range parts {
		cur = filepath.Join(cur, p)
		var err error
		if info, err = os.Lstat(cur); err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q is a symlink", ErrInvalidPath, relPath)
		}
	}
	if info.Mode().IsRegular() {
		if !isGGUF(rel) {
			return fmt.Errorf("%w: %q", ErrNotModel, relPath)
		}
		return os.Remove(cur)
	}
	if !info.IsDir() || len(parts) != 1 {
		return fmt.Errorf("%w: %q", ErrNotModel, relPath)
	}
	entries, err := os.ReadDir(cur)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("%w: %q is empty", ErrNotModel, relPath)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !isGGUF(e.Name()) {
			return fmt.Errorf("%w: %q holds %q", ErrNotModel, relPath, e.Name())
		}
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(cur, e.Name())); err != nil {
			return err
		}
	}
	return os.Remove(cur)
}

// Disk is the models directory's storage use for the Library's disk panel.
type Disk struct {
	GGUFBytes  int64 `json:"gguf_bytes"`
	FreeBytes  int64 `json:"free_bytes"`
	TotalBytes int64 `json:"total_bytes"`
}

// DiskUsage sums the GGUF bytes under modelsDir and reports the filesystem's free and total space.
func DiskUsage(modelsDir string) (Disk, error) {
	var d Disk
	var st syscall.Statfs_t
	if err := syscall.Statfs(modelsDir, &st); err != nil {
		return d, err
	}
	d.FreeBytes = int64(st.Bavail) * int64(st.Bsize)
	d.TotalBytes = int64(st.Blocks) * int64(st.Bsize)
	err := filepath.WalkDir(modelsDir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() && isGGUF(e.Name()) {
			if info, err := e.Info(); err == nil {
				d.GGUFBytes += info.Size()
			}
		}
		return nil
	})
	return d, err
}

// freeBytes is the free space on the filesystem holding path.
func freeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
