package plantools_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zeabur/zbplan/internal/plantools"
	"github.com/zeabur/zbplan/pkg/registryutil"
)

// mockFinder implements registryutil.Finder for testing.
type mockFinder struct {
	imagesFn func(ctx context.Context, registry, query string, limit int) ([]registryutil.Image, error)
	tagsFn   func(ctx context.Context, registry, image, keyword string, limit int) ([]registryutil.Tag, error)
}

func (m *mockFinder) Images(ctx context.Context, registry, query string, limit int) ([]registryutil.Image, error) {
	if m.imagesFn != nil {
		return m.imagesFn(ctx, registry, query, limit)
	}
	return nil, nil
}

func (m *mockFinder) Tags(ctx context.Context, registry, image, keyword string, limit int) ([]registryutil.Tag, error) {
	if m.tagsFn != nil {
		return m.tagsFn(ctx, registry, image, keyword, limit)
	}
	return nil, nil
}

// ListImages tests

var defaultSearchRegistries = []string{registryutil.RegistryDockerHub, registryutil.RegistryGHCR}

func TestListImages_SearchesOnlyGivenRegistries(t *testing.T) {
	t.Parallel()

	allowed, err := registryutil.NormalizeAllowedRegistries([]string{"ghcr.io", "registry.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	searchable := registryutil.SearchableRegistries(allowed)
	f := &mockFinder{
		imagesFn: func(_ context.Context, registry, _ string, _ int) ([]registryutil.Image, error) {
			if registry != "ghcr.io" {
				t.Errorf("searched registry %q outside the allowlist", registry)
			}
			return []registryutil.Image{{Registry: registry, Name: "image"}}, nil
		},
	}
	results, err := plantools.ListImages(context.Background(), f, searchable, "image")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Registry != "ghcr.io" {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestListImages_ReturnsCombinedResults(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, registry, _ string, _ int) ([]registryutil.Image, error) {
			return []registryutil.Image{
				{Registry: registry, Name: "myimage", Description: "desc"},
			}, nil
		},
	}

	results, err := plantools.ListImages(context.Background(), f, defaultSearchRegistries, "myimage")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (one per registry), got %d", len(results))
	}
}

func TestListImages_RegistryError_PartialResults(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, registry, _ string, _ int) ([]registryutil.Image, error) {
			if registry == "docker.io" {
				return nil, errors.New("docker.io unavailable")
			}
			return []registryutil.Image{
				{Registry: registry, Name: "ghcrimage"},
			}, nil
		},
	}

	results, err := plantools.ListImages(context.Background(), f, defaultSearchRegistries, "image")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result from successful registry, got %d", len(results))
	}
	if results[0].Registry != "ghcr.io" {
		t.Errorf("expected result from ghcr.io, got %q", results[0].Registry)
	}
}

func TestListImages_BothRegistriesError_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, _, _ string, _ int) ([]registryutil.Image, error) {
			return nil, errors.New("registry unreachable")
		},
	}

	results, err := plantools.ListImages(context.Background(), f, defaultSearchRegistries, "image")
	if err != nil {
		t.Fatalf("ListImages should not propagate registry errors, got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results when all registries fail, got %d", len(results))
	}
}

func TestListImages_NoImages_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, _, _ string, _ int) ([]registryutil.Image, error) {
			return nil, nil
		},
	}

	results, err := plantools.ListImages(context.Background(), f, defaultSearchRegistries, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestListImages_CancelledContext_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, registry, _ string, _ int) ([]registryutil.Image, error) {
			return []registryutil.Image{{Registry: registry, Name: "image"}}, nil
		},
	}

	results, err := plantools.ListImages(ctx, f, defaultSearchRegistries, "image")
	if err != nil {
		t.Fatalf("unexpected error with cancelled context: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results with cancelled context, got %d", len(results))
	}
}

// ListTags tests

func TestListTags_ReturnsTags(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		tagsFn: func(_ context.Context, _, _, _ string, _ int) ([]registryutil.Tag, error) {
			return []registryutil.Tag{{Name: "1.22"}, {Name: "1.22.0"}}, nil
		},
	}

	tags, err := plantools.ListTags(context.Background(), f, nil, "docker.io", "golang", "1.22")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(tags))
	}
}

func TestListTags_EmptyQuery_DefaultsToLatest(t *testing.T) {
	t.Parallel()

	var gotKeyword string
	f := &mockFinder{
		tagsFn: func(_ context.Context, _, _, keyword string, _ int) ([]registryutil.Tag, error) {
			gotKeyword = keyword
			return []registryutil.Tag{{Name: "latest"}}, nil
		},
	}

	_, err := plantools.ListTags(context.Background(), f, nil, "docker.io", "golang", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKeyword != "latest" {
		t.Errorf("expected keyword 'latest', got %q", gotKeyword)
	}
}

func TestListTags_FinderError_ReturnsError(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		tagsFn: func(_ context.Context, _, _, _ string, _ int) ([]registryutil.Tag, error) {
			return nil, errors.New("tags unavailable")
		},
	}

	_, err := plantools.ListTags(context.Background(), f, nil, "docker.io", "golang", "latest")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestListTags_RejectsUntrustedRegistry(t *testing.T) {
	t.Parallel()

	called := false
	f := &mockFinder{
		tagsFn: func(_ context.Context, _, _, _ string, _ int) ([]registryutil.Tag, error) {
			called = true
			return nil, nil
		},
	}

	_, err := plantools.ListTags(context.Background(), f, nil, "127.0.0.1:5000", "probe", "latest")
	if err == nil {
		t.Fatal("expected untrusted registry to be rejected")
	}
	if called {
		t.Fatal("finder was called for an untrusted registry")
	}
}

func TestListTags_AllowsConfiguredRegistry(t *testing.T) {
	t.Parallel()

	called := false
	f := &mockFinder{
		tagsFn: func(_ context.Context, registry, _, _ string, _ int) ([]registryutil.Tag, error) {
			called = true
			if registry != "registry.example.com" {
				t.Fatalf("registry = %q", registry)
			}
			return []registryutil.Tag{{Name: "latest"}}, nil
		},
	}

	if _, err := plantools.ListTags(
		context.Background(),
		f,
		[]string{"registry.example.com"},
		"registry.example.com",
		"team/image",
		"latest",
	); err != nil {
		t.Fatalf("configured registry was rejected: %v", err)
	}
	if !called {
		t.Fatal("finder was not called for configured registry")
	}
}

func TestListTags_RejectsInvalidImagePath(t *testing.T) {
	t.Parallel()

	called := false
	f := &mockFinder{
		tagsFn: func(_ context.Context, _, _, _ string, _ int) ([]registryutil.Tag, error) {
			called = true
			return nil, nil
		},
	}

	if _, err := plantools.ListTags(context.Background(), f, nil, "docker.io", "../admin", "latest"); err == nil {
		t.Fatal("expected invalid image path to be rejected")
	}
	if called {
		t.Fatal("finder was called for an invalid image path")
	}
}
