package registryutil

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
)

func TestCachedTagNames_ReusesResults(t *testing.T) {
	f := NewFinder()
	repo := mustRepository(t, "docker.io/library/ubuntu")
	calls := 0
	stubListRemoteTags(t, func(context.Context, name.Repository) ([]string, error) {
		calls++
		return []string{"noble", "jammy"}, nil
	})

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
	f := NewFinder()
	repo := mustRepository(t, "docker.io/library/ubuntu")
	calls := 0
	stubListRemoteTags(t, func(context.Context, name.Repository) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("registry unavailable")
		}
		return []string{"noble"}, nil
	})

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

func stubListRemoteTags(t *testing.T, fn func(context.Context, name.Repository) ([]string, error)) {
	t.Helper()

	original := listRemoteTags
	listRemoteTags = fn
	t.Cleanup(func() {
		listRemoteTags = original
	})
}

func mustRepository(t *testing.T, ref string) name.Repository {
	t.Helper()

	repo, err := name.NewRepository(ref)
	if err != nil {
		t.Fatalf("name.NewRepository(%q): %v", ref, err)
	}
	return repo
}
