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

func TestListImages_ReturnsCombinedResults(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, registry, _ string, _ int) ([]registryutil.Image, error) {
			return []registryutil.Image{
				{Registry: registry, Name: "myimage", Description: "desc"},
			}, nil
		},
	}

	results, err := plantools.ListImages(context.Background(), f, "myimage")
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

	results, err := plantools.ListImages(context.Background(), f, "image")
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

	results, err := plantools.ListImages(context.Background(), f, "image")
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

	results, err := plantools.ListImages(context.Background(), f, "nonexistent")
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

	results, err := plantools.ListImages(ctx, f, "image")
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

	tags, err := plantools.ListTags(context.Background(), f, "docker.io", "golang", "1.22")
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

	_, err := plantools.ListTags(context.Background(), f, "docker.io", "golang", "")
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

	_, err := plantools.ListTags(context.Background(), f, "docker.io", "golang", "latest")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
