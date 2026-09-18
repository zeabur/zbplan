// Package buildenvtest is the shared integration contract for both builder
// entry points. It uses dummy credentials and a local HTTP package server.
package buildenvtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/moby/buildkit/client"
	"github.com/tonistiigi/fsutil"
)

type Build func(context.Context, string, string, map[string]string, io.Writer) ([]byte, error)

// Run checks the actual build/OCI/runtime boundary, including a deliberately
// vulnerable baseline so a scanner that detects nothing cannot pass the suite.
func Run(t *testing.T, build Build) {
	t.Helper()
	addr := os.Getenv("BUILDKIT_HOST")
	if addr == "" {
		t.Skip("set BUILDKIT_HOST to an isolated local test daemon")
	}
	const first = "FAKE_BUILD_TOKEN_742dc975"
	const second = "FAKE_BUILD_TOKEN_d10c793f"
	const runtime = "FAKE_RUNTIME_TOKEN_5ae89b6c"
	const publicURL = "https://public-config.example.test"
	var passwordBytes [32]byte
	if _, err := rand.Read(passwordBytes[:]); err != nil {
		t.Fatal(err)
	}
	password := fmt.Sprintf("FAKE_BUILD_PASSWORD_%x", passwordBytes)
	var expected atomic.Value
	expected.Store(first)
	var requests atomic.Int32
	var passwordRequests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/password" {
			if r.Header.Get("X-Build-Password") != password {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			passwordRequests.Add(1)
			_, _ = io.WriteString(w, "password-authenticated\n")
			return
		}
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+expected.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "private-test-dependency-v1\n")
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	host := os.Getenv("BUILD_ENV_TEST_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	port := listener.Addr().(*net.TCPAddr).Port
	base := os.Getenv("BUILD_ENV_TEST_BASE")
	if base == "" {
		base = "alpine:3.22"
	}
	dockerfile := fmt.Sprintf(`FROM %s AS build
RUN wget -q --header="Authorization: Bearer $TOKEN" -O /dependency http://%s:%d/package
RUN printf '%%s' "$PUBLIC_URL" >/public-config
FROM %s
COPY --from=build /dependency /dependency
COPY --from=build /public-config /public-config
ENV MODE=production
CMD ["/bin/sh", "-c", "test -n \"$TOKEN\" && printf '%%s' \"$TOKEN\""]
`, base, host, port, base)
	contextDir := t.TempDir()
	var progress bytes.Buffer
	vars := map[string]string{"TOKEN": first, "PUBLIC_URL": publicURL, "UNUSED_SECRET": "FAKE_UNUSED_TOKEN_9814ba"}
	old, err := legacyBuild(t.Context(), addr, dockerfile, contextDir, vars)
	if err != nil {
		t.Fatal(err)
	}
	oldImage := inspect(t, old, first)
	if !oldImage.metadataLeak {
		t.Fatal("scanner failed to detect legacy metadata leak")
	}
	oldID := load(t, old, oldImage.configDigest)
	if out, err := docker(t.Context(), "run", "--rm", oldID); err != nil || string(out) != first {
		t.Fatalf("legacy fallback not reproduced: %s %v", out, err)
	}
	newImage, err := build(t.Context(), dockerfile, contextDir, vars, &progress)
	if err != nil {
		t.Fatalf("secure build failed: %v\n%s", err, progress.String())
	}
	newInfo := inspect(t, newImage, first)
	if newInfo.metadataLeak || newInfo.layerLeak {
		t.Fatal("build secret persisted in OCI output")
	}
	unused := inspect(t, newImage, vars["UNUSED_SECRET"])
	if unused.metadataLeak || unused.layerLeak {
		t.Fatal("unused secret persisted")
	}
	if strings.Contains(progress.String(), first) {
		t.Fatal("build secret appeared in progress logs")
	}
	newID := load(t, newImage, newInfo.configDigest)
	for _, image := range []string{oldID, newID} {
		out, err := docker(t.Context(), "run", "--rm", "--env", "TOKEN="+runtime, image)
		if err != nil || string(out) != runtime {
			t.Fatalf("runtime environment: %s %v", out, err)
		}
		out, err = docker(t.Context(), "run", "--rm", image, "cat", "/dependency")
		if err != nil || string(out) != "private-test-dependency-v1\n" {
			t.Fatalf("dependency changed: %s %v", out, err)
		}
		out, err = docker(t.Context(), "run", "--rm", image, "printenv", "MODE")
		if err != nil || string(out) != "production\n" {
			t.Fatalf("explicit image ENV changed: %s %v", out, err)
		}
		out, err = docker(t.Context(), "run", "--rm", image, "cat", "/public-config")
		if err != nil || string(out) != publicURL {
			t.Fatalf("compiled public build configuration changed: %s %v", out, err)
		}
	}
	if _, err := docker(t.Context(), "run", "--rm", newID, "printenv", "PUBLIC_URL"); err == nil {
		t.Fatal("public build variable unexpectedly became a runtime environment default")
	}
	if _, err := docker(t.Context(), "run", "--rm", newID); err == nil {
		t.Fatal("missing runtime TOKEN unexpectedly inherited build input")
	}
	if _, err := docker(t.Context(), "run", "--rm", "--env", "TOKEN=", newID); err == nil {
		t.Fatal("empty runtime TOKEN unexpectedly inherited build input")
	}
	count := requests.Load()
	if _, err := build(t.Context(), dockerfile, contextDir, vars, &progress); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != count {
		t.Fatal("identical inputs did not reuse cache")
	}
	expected.Store(second)
	vars["TOKEN"] = second
	changed, err := build(t.Context(), dockerfile, contextDir, vars, &progress)
	if err != nil {
		t.Fatalf("changed credential did not reach build: %v\n%s", err, progress.String())
	}
	if requests.Load() <= count {
		t.Fatal("changed build variable reused stale cache")
	}
	for _, token := range []string{first, second} {
		info := inspect(t, changed, token)
		if info.metadataLeak || info.layerLeak || strings.Contains(progress.String(), token) {
			t.Fatal("changed credential leaked")
		}
	}
	t.Run("password-readable-during-build-absent-from-image", func(t *testing.T) {
		// The expected password exists only in the test server and secret input,
		// never in the Dockerfile, its command arguments, or generated files.
		fetch := fmt.Sprintf(`wget -q --header="X-Build-Password: $PASSWORD" -O /proof http://%s:%d/password`, host, port)
		execCommand, _ := json.Marshal([]string{"/bin/sh", "-ec", fetch})
		source := fmt.Sprintf("FROM %s AS build\nRUN %s\nFROM %s\nRUN %s\nCOPY --from=build /proof /build-stage-proof\n", base, fetch, base, execCommand)
		for _, test := range []struct {
			name   string
			values map[string]string
		}{
			{"missing", nil},
			{"wrong", map[string]string{"PASSWORD": "FAKE_WRONG_PASSWORD"}},
		} {
			t.Run(test.name, func(t *testing.T) {
				var log bytes.Buffer
				if _, err := build(t.Context(), source, contextDir, test.values, &log); err == nil {
					t.Fatal("build succeeded without the required password")
				}
			})
		}
		before := passwordRequests.Load()
		var log bytes.Buffer
		output, err := build(t.Context(), source, contextDir, map[string]string{"PASSWORD": password}, &log)
		if err != nil {
			t.Fatalf("build could not authenticate with PASSWORD: %v", err)
		}
		if passwordRequests.Load()-before != 2 {
			t.Fatal("both the shell RUN and exec RUN must authenticate with the exact password")
		}
		info := inspect(t, output, password)
		if info.metadataLeak || info.layerLeak || strings.Contains(log.String(), password) {
			t.Fatal("password persisted in image metadata, layers or progress output")
		}
		id := load(t, output, info.configDigest)
		for _, proof := range []string{"/proof", "/build-stage-proof"} {
			out, err := docker(t.Context(), "run", "--rm", id, "cat", proof)
			if err != nil || string(out) != "password-authenticated\n" {
				t.Fatal("missing authenticated build output")
			}
		}
		if out, err := docker(t.Context(), "run", "--rm", id, "/bin/sh", "-c", `test "${PASSWORD+x}" != x`); err != nil {
			t.Fatalf("runtime unexpectedly contains PASSWORD: %s %v", out, err)
		}
		t.Log("exact password authenticated in shell and exec RUN; missing/wrong passwords rejected; full OCI and runtime contain no password value")
	})
	t.Run("password-layer-scanner-positive-control", func(t *testing.T) {
		// A later deletion cannot remove a value from an earlier image layer.
		// Verify the scanner catches this, even though the final filesystem is clean.
		source := fmt.Sprintf("FROM %s\nRUN printf '%%s' \"$PASSWORD\" >/password-file\nRUN rm /password-file\n", base)
		var log bytes.Buffer
		output, err := build(t.Context(), source, contextDir, map[string]string{"PASSWORD": password}, &log)
		if err != nil {
			t.Fatal(err)
		}
		info := inspect(t, output, password)
		if !info.layerLeak {
			t.Fatal("scanner missed a password in a deleted file's earlier layer")
		}
		id := load(t, output, info.configDigest)
		if _, err := docker(t.Context(), "run", "--rm", id, "test", "!", "-e", "/password-file"); err != nil {
			t.Fatal("positive control must remove the file from the final filesystem")
		}
	})
	t.Run("older-standard-frontend", func(t *testing.T) {
		image, err := build(t.Context(), "# syntax=docker/dockerfile:1.4\n"+dockerfile, contextDir, vars, &progress)
		if err != nil {
			t.Fatalf("older standard frontend: %v\n%s", err, progress.String())
		}
		info := inspect(t, image, second)
		if info.metadataLeak || info.layerLeak {
			t.Fatal("older frontend leaked build input")
		}
	})
	t.Run("special-values-and-exec", func(t *testing.T) {
		values := map[string]string{
			"TOKEN":           "FAKE_'quoted_\"dollar$\\_多行\nsecond-line_479a",
			"EMPTY":           "",
			"auth.jwt.secret": "FAKE_dotted-name_83bc",
		}
		var checks strings.Builder
		for _, key := range []string{"TOKEN", "EMPTY", "auth.jwt.secret"} {
			digest := sha256.Sum256([]byte(values[key] + "\n"))
			fmt.Fprintf(&checks, "test \"$(printenv '%s' | sha256sum | cut -d' ' -f1)\" = '%x' || exit 1\n", key, digest)
		}
		command, _ := json.Marshal([]string{"/bin/sh", "-c", checks.String()})
		source := fmt.Sprintf("FROM %s\nRUN %s\nRUN <<'EOF'\n%sEOF\nENV TOKEN=explicit-default\nRUN test \"$TOKEN\" = explicit-default\n", base, command, checks.String())
		image, err := build(t.Context(), source, contextDir, values, &progress)
		if err != nil {
			t.Fatalf("special values: %v\n%s", err, progress.String())
		}
		for _, key := range []string{"TOKEN", "auth.jwt.secret"} {
			info := inspect(t, image, values[key])
			if info.metadataLeak || info.layerLeak {
				t.Fatal("special value persisted")
			}
		}
		id := load(t, image, inspect(t, image, values["TOKEN"]).configDigest)
		out, err := docker(t.Context(), "run", "--rm", id, "printenv", "TOKEN")
		if err != nil || string(out) != "explicit-default\n" {
			t.Fatalf("explicit ENV was lost: %s %v", out, err)
		}
	})
	if !t.Failed() {
		t.Log("legacy leak reproduced; private dependency auth, secret-free OCI, runtime isolation, explicit ENV and cache invalidation passed")
	}
}

type imageInfo struct {
	configDigest            string
	metadataLeak, layerLeak bool
}

func inspect(t *testing.T, data []byte, marker string) imageInfo {
	t.Helper()
	files := map[string][]byte{}
	r := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		contents, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		files[strings.TrimPrefix(h.Name, "./")] = contents
	}
	type descriptor struct {
		Digest    string `json:"digest"`
		MediaType string `json:"mediaType"`
	}
	var index struct {
		Manifests []descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(files["index.json"], &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 1 {
		t.Fatalf("expected one manifest, got %d", len(index.Manifests))
	}
	blob := func(digest string) []byte { return files["blobs/"+strings.ReplaceAll(digest, ":", "/")] }
	var manifest struct {
		Config descriptor   `json:"config"`
		Layers []descriptor `json:"layers"`
	}
	if err := json.Unmarshal(blob(index.Manifests[0].Digest), &manifest); err != nil {
		t.Fatal(err)
	}
	info := imageInfo{configDigest: manifest.Config.Digest}
	quoted, _ := json.Marshal(marker)
	leaks := func(data []byte) bool {
		return bytes.Contains(data, []byte(marker)) || bytes.Contains(data, quoted[1:len(quoted)-1])
	}
	layers := map[string]bool{}
	for _, layer := range manifest.Layers {
		name := "blobs/" + strings.ReplaceAll(layer.Digest, ":", "/")
		layers[name] = true
		contents := files[name]
		if strings.Contains(layer.MediaType, "gzip") {
			gz, err := gzip.NewReader(bytes.NewReader(contents))
			if err != nil {
				t.Fatal(err)
			}
			contents, err = io.ReadAll(gz)
			_ = gz.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else if strings.Contains(layer.MediaType, "zstd") {
			t.Fatal("add zstd decoding before accepting this export")
		}
		info.layerLeak = info.layerLeak || leaks(contents)
	}
	// Inspect every non-layer blob, including unreferenced blobs, not just Config.Env.
	for name, contents := range files {
		if !layers[name] && leaks(contents) {
			info.metadataLeak = true
		}
	}
	return info
}

func load(t *testing.T, image []byte, digest string) string {
	t.Helper()
	dir := t.TempDir()
	r := tar.NewReader(bytes.NewReader(image))
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if !filepath.IsLocal(h.Name) {
			t.Fatal("non-local OCI path")
		}
		path := filepath.Join(dir, h.Name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oci, err := layout.FromPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	index, err := oci.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := index.IndexManifest()
	if err != nil || len(manifest.Manifests) != 1 {
		t.Fatal("invalid OCI index", err)
	}
	img, err := oci.Image(manifest.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference("build-env-test:" + strings.TrimPrefix(digest, "sha256:") + fmt.Sprintf("-%x", suffix))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "docker.tar")
	if err := tarball.WriteToFile(path, ref, img); err != nil {
		t.Fatal(err)
	}
	if out, err := docker(t.Context(), "load", "--input", path); err != nil {
		t.Fatalf("load image: %s %v", out, err)
	}
	t.Cleanup(func() { _, _ = docker(context.Background(), "image", "rm", ref.Name()) })
	return ref.Name()
}

func docker(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

type bufferCloser struct{ bytes.Buffer }

func (b *bufferCloser) Close() error { return nil }

func legacyBuild(ctx context.Context, addr, dockerfile, contextDir string, values map[string]string) ([]byte, error) {
	var declarations strings.Builder
	attrs := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		fmt.Fprintf(&declarations, "\nARG ZEABUR_ENV_%s\nENV %s=${ZEABUR_ENV_%s}\n", key, key, key)
		attrs["build-arg:ZEABUR_ENV_"+key] = values[key]
	}
	lines := strings.Split(dockerfile, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "FROM ") {
			lines[i] += declarations.String()
		}
	}
	dir, err := os.MkdirTemp("", "build-env-legacy-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return nil, err
	}
	ctxFS, err := fsutil.NewFS(contextDir)
	if err != nil {
		return nil, err
	}
	dfFS, err := fsutil.NewFS(dir)
	if err != nil {
		return nil, err
	}
	c, err := client.New(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	var output bufferCloser
	_, err = c.Solve(ctx, nil, client.SolveOpt{
		Frontend: "dockerfile.v0", FrontendAttrs: attrs,
		LocalMounts: map[string]fsutil.FS{"context": ctxFS, "dockerfile": dfFS},
		Exports:     []client.ExportEntry{{Type: client.ExporterOCI, Output: func(map[string]string) (io.WriteCloser, error) { return &output, nil }}},
	}, nil)
	return output.Bytes(), err
}
