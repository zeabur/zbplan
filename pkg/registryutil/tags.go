package registryutil

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/lithammer/fuzzysearch/fuzzy"
)

// Tags returns up to limit tags from registry/image whose names best
// match keyword, sorted by CreatedAt descending.
//
//   - registry: "docker.io", "ghcr.io", etc. Empty defaults to "docker.io".
//   - image: "ubuntu" → "library/ubuntu" (docker.io only); "owner/repo" kept as-is.
//   - keyword: e.g. "24.04"
//   - limit: e.g. 5
func (f *finder) Tags(ctx context.Context, registry, image, keyword string, limit int) ([]Tag, error) {
	if keyword == "" {
		return nil, fmt.Errorf("keyword must not be empty")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}
	if registry == "" {
		registry = RegistryDockerHub
	}

	image = normalizeImage(registry, image)
	repoRef := registry + "/" + image

	repo, err := name.NewRepository(repoRef)
	if err != nil {
		return nil, fmt.Errorf("invalid repository %q: %w", repoRef, err)
	}

	tagNames, err := f.cachedTagNames(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("listing tags for %s: %w", repoRef, err)
	}

	ranked := fuzzy.RankFindFold(keyword, tagNames)
	sort.Sort(ranked)

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	plOS, plArch := f.platform()
	tags := make([]Tag, 0, len(ranked))
	for _, r := range ranked {
		t, err := f.cachedCreatedAt(ctx, repo, r.Target, plOS, plArch)
		if err != nil {
			// skip tags whose timestamp can't be resolved
			continue
		}
		tags = append(tags, Tag{Name: r.Target, CreatedAt: t})
	}

	sort.Slice(tags, func(i, j int) bool {
		return tags[i].CreatedAt.After(tags[j].CreatedAt)
	})

	return tags, nil
}

func normalizeImage(registry, image string) string {
	image = strings.Trim(image, "/")
	if registry == RegistryDockerHub && !strings.Contains(image, "/") {
		return "library/" + image
	}
	return image
}

func defaultListRemoteTags(ctx context.Context, repo name.Repository) ([]string, error) {
	return remote.List(repo,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
}

func (f *finder) cachedTagNames(ctx context.Context, repo name.Repository) ([]string, error) {
	key := repo.Name()
	if tagNames, ok := f.tagNamesCache.Get(key); ok {
		return slices.Clone(tagNames), nil
	}

	ch := f.tagNamesGroup.DoChan(key, func() (any, error) {
		if tagNames, ok := f.tagNamesCache.Get(key); ok {
			return tagNames, nil
		}
		tagNames, err := f.listRemoteTags(context.Background(), repo)
		if err != nil {
			return nil, err
		}
		tagNames = slices.Clone(tagNames)
		f.tagNamesCache.Add(key, tagNames)
		return tagNames, nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return slices.Clone(res.Val.([]string)), nil
	}
}

func (f *finder) cachedCreatedAt(ctx context.Context, repo name.Repository, tagName, plOS, plArch string) (time.Time, error) {
	key := repo.Name() + ":" + tagName + "@" + plOS + "/" + plArch
	if createdAt, ok := f.tagCreatedAtCache.Get(key); ok {
		return createdAt, nil
	}

	ch := f.tagCreatedAtGroup.DoChan(key, func() (any, error) {
		if createdAt, ok := f.tagCreatedAtCache.Get(key); ok {
			return createdAt, nil
		}
		createdAt, err := f.resolveTagCreatedAt(context.Background(), repo, tagName, plOS, plArch)
		if err != nil {
			return time.Time{}, err
		}
		f.tagCreatedAtCache.Add(key, createdAt)
		return createdAt, nil
	})

	select {
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return time.Time{}, res.Err
		}
		return res.Val.(time.Time), nil
	}
}

func resolveCreatedAt(ctx context.Context, repo name.Repository, tagName, plOS, plArch string) (time.Time, error) {
	ref := repo.Tag(tagName)
	desc, err := remote.Get(ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		return time.Time{}, err
	}

	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return time.Time{}, err
		}
		manifest, err := idx.IndexManifest()
		if err != nil {
			return time.Time{}, err
		}
		if len(manifest.Manifests) == 0 {
			return time.Time{}, fmt.Errorf("empty index for %s:%s", repo, tagName)
		}
		// prefer the requested platform, fall back to first entry
		chosen := manifest.Manifests[0]
		for _, m := range manifest.Manifests {
			if m.Platform != nil && m.Platform.OS == plOS && m.Platform.Architecture == plArch {
				chosen = m
				break
			}
		}
		childRef := repo.Digest(chosen.Digest.String())
		childDesc, err := remote.Get(childRef,
			remote.WithContext(ctx),
			remote.WithAuthFromKeychain(authn.DefaultKeychain),
		)
		if err != nil {
			return time.Time{}, err
		}
		image, err := childDesc.Image()
		if err != nil {
			return time.Time{}, err
		}
		cfg, err := image.ConfigFile()
		if err != nil {
			return time.Time{}, err
		}
		return cfg.Created.Time, nil
	}

	image, err := desc.Image()
	if err != nil {
		return time.Time{}, err
	}
	cfg, err := image.ConfigFile()
	if err != nil {
		return time.Time{}, err
	}
	return cfg.Created.Time, nil
}
