package builder

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/tonistiigi/fsutil"
)

// filteredFS hides paths from a build context on both the walk BuildKit uses
// to enumerate the context and on direct opens, so an excluded file is never
// transferred to the daemon.
type filteredFS struct {
	fs      fsutil.FS
	exclude func(path string, isDir bool) bool
}

func newFilteredFS(base fsutil.FS, exclude func(path string, isDir bool) bool) fsutil.FS {
	if exclude == nil {
		return base
	}
	return &filteredFS{fs: base, exclude: exclude}
}

func (f *filteredFS) Walk(ctx context.Context, target string, fn fs.WalkDirFunc) error {
	return f.fs.Walk(ctx, target, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil {
			return fn(p, entry, err)
		}
		if f.exclude(filepath.ToSlash(p), entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return fn(p, entry, nil)
	})
}

func (f *filteredFS) Open(p string) (io.ReadCloser, error) {
	clean := path.Clean(filepath.ToSlash(p))
	if f.exclude(clean, false) {
		return nil, fmt.Errorf("open %s: %w", p, os.ErrNotExist)
	}
	for dir := path.Dir(clean); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if f.exclude(dir, true) {
			return nil, fmt.Errorf("open %s: %w", p, os.ErrNotExist)
		}
	}
	return f.fs.Open(p)
}
