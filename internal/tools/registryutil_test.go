package tools_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zeabur/zbplan/internal/tools"
	"github.com/zeabur/zbplan/lib/registryutil"
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

func TestFindImage_ReturnsCombinedResults(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, registry, _ string, _ int) ([]registryutil.Image, error) {
			return []registryutil.Image{
				{Registry: registry, Name: "myimage", Description: "desc"},
			}, nil
		},
		tagsFn: func(_ context.Context, _, _ string, _ string, _ int) ([]registryutil.Tag, error) {
			return []registryutil.Tag{{Name: "latest"}}, nil
		},
	}

	results, err := tools.ListImagesAndTags(context.Background(), f, "myimage", "latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both "docker.io" and "ghcr.io" should each return one entry.
	if len(results) != 2 {
		t.Errorf("expected 2 results (one per registry), got %d", len(results))
	}
	for _, r := range results {
		if len(r.Tags) == 0 {
			t.Errorf("expected tags for %s/%s, got none", r.Registry, r.Name)
		}
	}
}

func TestFindImage_EmptyVersion_ReturnsResults(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, registry, _ string, _ int) ([]registryutil.Image, error) {
			return []registryutil.Image{
				{Registry: registry, Name: "nginx"},
			}, nil
		},
		tagsFn: func(_ context.Context, _, _ string, _ string, _ int) ([]registryutil.Tag, error) {
			return []registryutil.Tag{{Name: "latest"}}, nil
		},
	}

	results, err := tools.ListImagesAndTags(context.Background(), f, "nginx", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results for empty version, got none")
	}
}

func TestFindImage_ImagesError_PartialResults(t *testing.T) {
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
		tagsFn: func(_ context.Context, _, _ string, _ string, _ int) ([]registryutil.Tag, error) {
			return []registryutil.Tag{{Name: "latest"}}, nil
		},
	}

	results, err := tools.ListImagesAndTags(context.Background(), f, "image", "")
	if err != nil {
		t.Fatalf("FindImage returned unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result from successful registry, got %d", len(results))
	}
	if results[0].Registry != "ghcr.io" {
		t.Errorf("expected result from ghcr.io, got %q", results[0].Registry)
	}
}

func TestFindImage_TagsError_ImageExcluded(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, registry, _ string, _ int) ([]registryutil.Image, error) {
			return []registryutil.Image{
				{Registry: registry, Name: "image1"},
				{Registry: registry, Name: "image2"},
			}, nil
		},
		tagsFn: func(_ context.Context, _, image string, _ string, _ int) ([]registryutil.Tag, error) {
			if image == "image1" {
				return nil, errors.New("tags unavailable")
			}
			return []registryutil.Tag{{Name: "latest"}}, nil
		},
	}

	results, err := tools.ListImagesAndTags(context.Background(), f, "image", "")
	if err != nil {
		t.Fatalf("FindImage returned unexpected error: %v", err)
	}
	// image1 fails tags on both registries; image2 succeeds on both.
	if len(results) != 2 {
		t.Errorf("expected 2 results (image2 from each registry), got %d", len(results))
	}
	for _, r := range results {
		if r.Name != "image2" {
			t.Errorf("expected only image2 in results, got %q", r.Name)
		}
	}
}

func TestFindImage_NoImages_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, _, _ string, _ int) ([]registryutil.Image, error) {
			return nil, nil
		},
	}

	results, err := tools.ListImagesAndTags(context.Background(), f, "nonexistent", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestFindImage_BothRegistriesError_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, _, _ string, _ int) ([]registryutil.Image, error) {
			return nil, errors.New("registry unreachable")
		},
	}

	results, err := tools.ListImagesAndTags(context.Background(), f, "image", "")
	if err != nil {
		t.Fatalf("FindImage should not propagate registry errors, got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results when all registries fail, got %d", len(results))
	}
}

func TestFindImage_CancelledContext_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &mockFinder{
		imagesFn: func(_ context.Context, _, _ string, _ int) ([]registryutil.Image, error) {
			return []registryutil.Image{{Name: "image"}}, nil
		},
	}

	results, err := tools.ListImagesAndTags(ctx, f, "image", "")
	if err != nil {
		t.Fatalf("unexpected error with cancelled context: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results with cancelled context, got %d", len(results))
	}
}
