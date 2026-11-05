package builds

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// ErrUnsafeArchive means an archive tried something a release has no reason to do.
var ErrUnsafeArchive = errors.New("unsafe archive")

const (
	archiveSuffix = ".tar.gz"
	maxEntries    = 1 << 16
)

// ctxReader stops long reads when the context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// extract unpacks a tar.gz into dest, removing the single top directory, which must be one of
// tops. It refuses whatever could write outside dest: paths with "..", absolute paths, links
// leaving the tree, and devices. os.Root is a second line of defense below those checks.
func extract(ctx context.Context, archive, dest string, tops []string, maxBytes int64) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(ctxReader{ctx, file})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsafeArchive, err)
	}
	defer func() { _ = gz.Close() }()
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	tr := tar.NewReader(gz)
	var total int64
	for entries := 0; ; entries++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %w", ErrUnsafeArchive, err)
		}
		if entries >= maxEntries {
			return fmt.Errorf("%w: more than %d entries", ErrUnsafeArchive, maxEntries)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		rel, err := entryPath(h.Name, tops)
		if err != nil {
			return err
		}
		if rel == "" {
			if h.Typeflag != tar.TypeDir {
				return fmt.Errorf("%w: %q is not in a top directory", ErrUnsafeArchive, h.Name)
			}
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = makeDirs(root, rel)
		case tar.TypeReg:
			if h.Size > maxBytes-total {
				return fmt.Errorf("%w: larger than %d bytes", ErrUnsafeArchive, maxBytes)
			}
			total += h.Size
			err = writeFile(root, rel, tr, h.FileInfo().Mode())
		case tar.TypeSymlink:
			err = writeSymlink(root, rel, h.Linkname)
		case tar.TypeLink:
			err = writeHardlink(root, rel, h.Linkname, tops)
		default:
			err = fmt.Errorf("%w: %q has type %q", ErrUnsafeArchive, h.Name, h.Typeflag)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("extract %q: %w", h.Name, err)
		}
	}
}

// entryPath validates a name and strips the top directory. It returns "" for the top directory.
func entryPath(name string, tops []string) (string, error) {
	if name == "" || path.IsAbs(name) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: bad path %q", ErrUnsafeArchive, name)
	}
	if slices.Contains(strings.Split(name, "/"), "..") {
		return "", fmt.Errorf("%w: path %q climbs up a directory", ErrUnsafeArchive, name)
	}
	top, rest, _ := strings.Cut(path.Clean(name), "/")
	if !slices.Contains(tops, top) {
		return "", fmt.Errorf("%w: %q is outside %v", ErrUnsafeArchive, name, tops)
	}
	return rest, nil
}

// makeDirs creates dir and its parents, refusing to pass through a symlink: a link's relative
// target is only checked against where the link appears to be.
func makeDirs(root *os.Root, dir string) error {
	cur := ""
	for part := range strings.SplitSeq(dir, "/") {
		cur = path.Join(cur, part)
		info, err := root.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := root.Mkdir(cur, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !info.IsDir():
			return fmt.Errorf("%w: %q is not a directory", ErrUnsafeArchive, cur)
		}
	}
	return nil
}

// prepare makes the parents of rel and clears a previous non-directory there: tar lets later
// entries replace earlier ones, and the CUDA runtime may repeat a file of the main archive.
func prepare(root *os.Root, rel string) error {
	if dir := path.Dir(rel); dir != "." {
		if err := makeDirs(root, dir); err != nil {
			return err
		}
	}
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%w: %q replaces a directory", ErrUnsafeArchive, rel)
	}
	return root.Remove(rel)
}

func writeFile(root *os.Root, rel string, r io.Reader, mode fs.FileMode) error {
	if err := prepare(root, rel); err != nil {
		return err
	}
	// Perm drops setuid, setgid and sticky.
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm()&0o755|0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func writeSymlink(root *os.Root, rel, target string) error {
	if err := checkSymlink(rel, target); err != nil {
		return err
	}
	if err := prepare(root, rel); err != nil {
		return err
	}
	return root.Symlink(target, rel)
}

// checkSymlink allows relative targets staying in the tree, with ".." only as a prefix. A later
// ".." would apply to wherever an earlier component's link leads, not to what it looks like.
func checkSymlink(rel, target string) error {
	if target == "" || path.IsAbs(target) || strings.ContainsRune(target, 0) {
		return fmt.Errorf("%w: link to %q", ErrUnsafeArchive, target)
	}
	depth := strings.Count(rel, "/")
	descended := false
	for part := range strings.SplitSeq(target, "/") {
		switch part {
		case "", ".":
		case "..":
			depth--
			if descended || depth < 0 {
				return fmt.Errorf("%w: link to %q leaves the tree", ErrUnsafeArchive, target)
			}
		default:
			descended = true
		}
	}
	return nil
}

// writeHardlink takes a target relative to the archive root, unlike a symlink's.
func writeHardlink(root *os.Root, rel, linkname string, tops []string) error {
	target, err := entryPath(linkname, tops)
	if err != nil {
		return err
	}
	if target == "" || target == rel {
		return fmt.Errorf("%w: hard link to %q", ErrUnsafeArchive, linkname)
	}
	info, err := root.Lstat(target)
	if err != nil {
		return fmt.Errorf("%w: hard link to %q: %w", ErrUnsafeArchive, linkname, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: hard link to non-file %q", ErrUnsafeArchive, linkname)
	}
	if err := prepare(root, rel); err != nil {
		return err
	}
	return root.Link(target, rel)
}
