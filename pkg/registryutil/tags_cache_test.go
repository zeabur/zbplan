package registryutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
)

func TestCachedTagNames_ReusesResults(t *testing.T) {
	calls := 0
	f := NewFinder(WithListRemoteTags(func(context.Context, name.Repository) ([]string, error) {
		calls++
		return []string{"noble", "jammy"}, nil
	})).(*finder)
	repo := mustRepository(t, "docker.io/library/ubuntu")

	first, err := f.cachedTagNames(context.Background(), repo)
	if err != nil {
		t.Fatalf("first cachedTagNames: %v", err)
	}
	first[0] = "mutated"

	second, err := f.cachedTagNames(context.Background(), repo)
	if err != nil {
		t.Fatalf("second cachedTagNames: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected loader to be called once, got %d", calls)
	}
	if second[0] != "noble" {
		t.Fatalf("expected cached slice to be isolated, got %q", second[0])
	}
}

func TestCachedTagNames_DoesNotCacheErrors(t *testing.T) {
	calls := 0
	f := NewFinder(WithListRemoteTags(func(context.Context, name.Repository) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("registry unavailable")
		}
		return []string{"noble"}, nil
	})).(*finder)
	repo := mustRepository(t, "docker.io/library/ubuntu")

	if _, err := f.cachedTagNames(context.Background(), repo); err == nil {
		t.Fatal("expected first call to return loader error")
	}
	tags, err := f.cachedTagNames(context.Background(), repo)
	if err != nil {
		t.Fatalf("second cachedTagNames: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected loader retry after error, got %d calls", calls)
	}
	if len(tags) != 1 || tags[0] != "noble" {
		t.Fatalf("unexpected tags: %#v", tags)
	}
}

func TestCachedCreatedAt_ReusesResults(t *testing.T) {
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	f := NewFinder(WithResolveTagCreatedAt(func(_ context.Context, _ name.Repository, _, _, _ string) (time.Time, error) {
		calls++
		return ts, nil
	})).(*finder)
	repo := mustRepository(t, "docker.io/library/ubuntu")

	first, err := f.cachedCreatedAt(context.Background(), repo, "noble", "linux", "amd64")
	if err != nil {
		t.Fatalf("first cachedCreatedAt: %v", err)
	}
	second, err := f.cachedCreatedAt(context.Background(), repo, "noble", "linux", "amd64")
	if err != nil {
		t.Fatalf("second cachedCreatedAt: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected resolver to be called once, got %d", calls)
	}
	if !first.Equal(ts) || !second.Equal(ts) {
		t.Fatalf("unexpected times: first=%v second=%v", first, second)
	}
}

func TestCachedCreatedAt_DoesNotCacheErrors(t *testing.T) {
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	f := NewFinder(WithResolveTagCreatedAt(func(_ context.Context, _ name.Repository, _, _, _ string) (time.Time, error) {
		calls++
		if calls == 1 {
			return time.Time{}, errors.New("registry unavailable")
		}
		return ts, nil
	})).(*finder)
	repo := mustRepository(t, "docker.io/library/ubuntu")

	if _, err := f.cachedCreatedAt(context.Background(), repo, "noble", "linux", "amd64"); err == nil {
		t.Fatal("expected first call to return resolver error")
	}
	got, err := f.cachedCreatedAt(context.Background(), repo, "noble", "linux", "amd64")
	if err != nil {
		t.Fatalf("second cachedCreatedAt: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected resolver retry after error, got %d calls", calls)
	}
	if !got.Equal(ts) {
		t.Fatalf("unexpected time: %v", got)
	}
}

func mustRepository(t *testing.T, ref string) name.Repository {
	t.Helper()

	repo, err := name.NewRepository(ref)
	if err != nil {
		t.Fatalf("name.NewRepository(%q): %v", ref, err)
	}
	return repo
}
