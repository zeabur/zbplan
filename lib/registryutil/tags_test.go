package registryutil_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/zeabur/zbplan/lib/registryutil"
)

// Tags — Docker Hub

func TestTags_DockerHub_ReturnsMatchingTags(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tags, err := finder.Tags(ctx, registryutil.RegistryDockerHub, "ubuntu", "noble", 5)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("expected at least one tag, got none")
	}
	for _, tag := range tags {
		if tag.CreatedAt.IsZero() {
			t.Errorf("tag %q has zero CreatedAt", tag.Name)
		}
	}
}

func TestTags_DockerHub_SortedNewestFirst(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tags, err := finder.Tags(ctx, registryutil.RegistryDockerHub, "ubuntu", "noble", 5)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if !sort.SliceIsSorted(tags, func(i, j int) bool {
		return tags[i].CreatedAt.After(tags[j].CreatedAt)
	}) {
		t.Error("tags are not sorted newest-first")
	}
}

func TestTags_DockerHub_LibraryPrefixNormalized(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// "ubuntu" and "library/ubuntu" must return identical results.
	tags1, err := finder.Tags(ctx, registryutil.RegistryDockerHub, "ubuntu", "noble", 3)
	if err != nil {
		t.Fatalf("Tags(ubuntu): %v", err)
	}
	tags2, err := finder.Tags(ctx, registryutil.RegistryDockerHub, "library/ubuntu", "noble", 3)
	if err != nil {
		t.Fatalf("Tags(library/ubuntu): %v", err)
	}
	if len(tags1) != len(tags2) {
		t.Fatalf("length mismatch: ubuntu=%d, library/ubuntu=%d", len(tags1), len(tags2))
	}
	for i := range tags1 {
		if tags1[i].Name != tags2[i].Name {
			t.Errorf("tag[%d] mismatch: %q vs %q", i, tags1[i].Name, tags2[i].Name)
		}
	}
}

func TestTags_DockerHub_LimitRespected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tags, err := finder.Tags(ctx, registryutil.RegistryDockerHub, "ubuntu", "noble", 3)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) > 3 {
		t.Errorf("expected at most 3 tags, got %d", len(tags))
	}
}

func TestTags_DockerHub_ARM64(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	f := registryutil.NewFinder(registryutil.WithPlatform("linux/arm64"))
	tags, err := f.Tags(ctx, registryutil.RegistryDockerHub, "ubuntu", "noble", 3)
	if err != nil {
		t.Fatalf("Tags(arm64): %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("expected at least one tag, got none")
	}
	for _, tag := range tags {
		if tag.CreatedAt.IsZero() {
			t.Errorf("tag %q has zero CreatedAt", tag.Name)
		}
	}
}

// Tags — ghcr.io

func TestTags_GHCR_ReturnsMatchingTags(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tags, err := finder.Tags(ctx, registryutil.RegistryGHCR, "astral-sh/uv", "0.5", 5)
	if err != nil {
		t.Fatalf("Tags(ghcr.io): %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("expected at least one tag, got none")
	}
	for _, tag := range tags {
		if tag.CreatedAt.IsZero() {
			t.Errorf("tag %q has zero CreatedAt", tag.Name)
		}
	}
}

// Tags — validation errors

func TestTags_EmptyKeyword_ReturnsError(t *testing.T) {
	t.Parallel()
	_, err := finder.Tags(context.Background(), "", "ubuntu", "", 5)
	if err == nil {
		t.Fatal("expected error for empty keyword, got nil")
	}
}

func TestTags_ZeroLimit_ReturnsError(t *testing.T) {
	t.Parallel()
	_, err := finder.Tags(context.Background(), "", "ubuntu", "noble", 0)
	if err == nil {
		t.Fatal("expected error for zero limit, got nil")
	}
}
