package builder

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tonistiigi/fsutil"
)

func TestFilteredFSHidesExcludedPathsFromWalkAndOpen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"app.go", ".env", "secrets/token", "src/main.go"} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base, err := fsutil.NewFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	filtered := newFilteredFS(base, func(path string, _ bool) bool {
		return path == ".env" || path == "secrets"
	})

	var walked []string
	err = filtered.Walk(context.Background(), "", func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		walked = append(walked, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(walked)
	if want := []string{"app.go", "src", "src/main.go"}; !slices.Equal(walked, want) {
		t.Fatalf("walked %v, want %v", walked, want)
	}

	for _, name := range []string{".env", "secrets/token", "./secrets/../.env"} {
		if _, err := filtered.Open(name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Open(%q) = %v, want not-exist", name, err)
		}
	}
	rc, err := filtered.Open("src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
}

func TestFilteredFSWithoutPredicateIsUnchanged(t *testing.T) {
	t.Parallel()

	base, err := fsutil.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := newFilteredFS(base, nil); got != base {
		t.Fatal("nil predicate should not wrap the filesystem")
	}
}
