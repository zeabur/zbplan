//go:build integration

package buildenv_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	"github.com/tonistiigi/fsutil"
	"github.com/zeabur/zbplan/pkg/buildenv"
)

func TestSecretAbsentFromProvenanceAndExportedCache(t *testing.T) {
	addr := os.Getenv("BUILDKIT_HOST")
	if addr == "" {
		t.Skip("set BUILDKIT_HOST to an isolated local test daemon")
	}
	const secret = "FAKE_CACHE_PROVENANCE_SECRET_43e08d"
	base := os.Getenv("BUILD_ENV_TEST_BASE")
	if base == "" {
		base = "alpine:3.22"
	}
	prepared, err := buildenv.Prepare(t.Context(), "FROM "+base+"\nRUN test -n \"$TOKEN\" && printf ok >/artifact\n", map[string]string{"TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(prepared.Dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := fsutil.NewFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(t.Context(), addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	cacheDir := t.TempDir()
	imageFile, err := os.CreateTemp(t.TempDir(), "image-*.tar")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = imageFile.Close() }()
	_, err = c.Solve(t.Context(), nil, client.SolveOpt{
		Frontend:      "dockerfile.v0",
		FrontendAttrs: map[string]string{"attest:provenance": "mode=max"},
		LocalMounts:   map[string]fsutil.FS{"context": local, "dockerfile": local},
		Session:       prepared.Session,
		Exports:       []client.ExportEntry{{Type: client.ExporterOCI, Output: func(map[string]string) (io.WriteCloser, error) { return imageFile, nil }}},
		CacheExports:  []client.CacheOptionsEntry{{Type: "local", Attrs: map[string]string{"dest": cacheDir, "mode": "max"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(data []byte) {
		if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
			gz, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gz.Close() }()
			data, err = io.ReadAll(gz)
			if err != nil {
				t.Fatal(err)
			}
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("secret in exported metadata or layer")
		}
	}
	data, err := os.ReadFile(imageFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(data))
	provenanceFound := false
	for {
		_, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		blob, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		check(blob)
		if bytes.Contains(blob, []byte("https://slsa.dev/provenance/")) {
			provenanceFound = true
		}
	}
	if !provenanceFound {
		t.Fatal("test did not actually export provenance")
	}
	cacheBlobs := 0
	err = filepath.WalkDir(cacheDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		check(blob)
		cacheBlobs++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cacheBlobs < 3 {
		t.Fatal("test did not actually export a build cache")
	}
}
