package registryutil

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	RegistryDockerHub = "docker.io"
	RegistryGHCR      = "ghcr.io"
	RegistryQuay      = "quay.io"
	RegistryGCR       = "gcr.io"

	defaultTagCacheTTL = 10 * time.Minute
	defaultTagCacheMax = 1024
	defaultHTTPTimeout = 30 * time.Second
)

// registryHostRE accepts a lowercase DNS host with an optional port. Allowlist
// entries become BuildKit source-policy selectors, so wildcard and path
// characters must never pass.
var registryHostRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*(?::[0-9]{1,5})?$`)

// NormalizeAllowedRegistries returns a lowercase, trimmed, de-duplicated
// registry allowlist. An empty list uses the supported public defaults.
func NormalizeAllowedRegistries(configured []string) ([]string, error) {
	if len(configured) == 0 {
		return []string{RegistryDockerHub, RegistryGHCR, RegistryQuay, RegistryGCR}, nil
	}

	seen := make(map[string]struct{}, len(configured))
	registries := make([]string, 0, len(configured))
	for _, registry := range configured {
		registry = strings.ToLower(strings.TrimSpace(registry))
		if registry == "" {
			continue
		}
		if !registryHostRE.MatchString(registry) {
			return nil, fmt.Errorf("invalid registry %q", registry)
		}
		parsed, err := name.NewRegistry(registry, name.StrictValidation)
		// go-containerregistry maps docker.io to index.docker.io internally.
		// Keep the public hostname used by image policy and tool allowlists.
		if err != nil || (parsed.Name() != registry && registry != RegistryDockerHub) {
			return nil, fmt.Errorf("invalid registry %q", registry)
		}
		if _, ok := seen[registry]; ok {
			continue
		}
		seen[registry] = struct{}{}
		registries = append(registries, registry)
	}
	if len(registries) == 0 {
		return nil, fmt.Errorf("registry allowlist is empty")
	}
	return registries, nil
}

// SearchableRegistries returns the registries in allowed whose image search
// API Images supports, in a stable order. allowed must already be normalized.
func SearchableRegistries(allowed []string) []string {
	var searchable []string
	for _, registry := range []string{RegistryDockerHub, RegistryGHCR} {
		if slices.Contains(allowed, registry) {
			searchable = append(searchable, registry)
		}
	}
	return searchable
}

type Tag struct {
	Name      string
	CreatedAt time.Time
}

type Finder interface {
	Images(ctx context.Context, registry, query string, limit int) ([]Image, error)
	Tags(ctx context.Context, registry, image, keyword string, limit int) ([]Tag, error)
}

type finder struct {
	// HTTPClient is used for Hub/GitHub search HTTP calls.
	// Registry v2 calls use go-containerregistry's own transport.
	HTTPClient *http.Client
	// Platform selects which platform manifest to read when resolving
	// timestamps from a multi-arch image index.
	// Format: "os/arch", e.g. "linux/amd64" or "linux/arm64".
	// Empty defaults to "linux/amd64".
	Platform string

	tagNamesCache     *expirable.LRU[string, []string]
	tagCreatedAtCache *expirable.LRU[string, time.Time]
	tagNamesGroup     sharedCalls[[]string]
	tagCreatedAtGroup sharedCalls[time.Time]

	listRemoteTags      func(context.Context, name.Repository) ([]string, error)
	resolveTagCreatedAt func(context.Context, name.Repository, string, string, string) (time.Time, error)
}

type FindOption func(*finder)

func WithPlatform(platform string) FindOption {
	return func(f *finder) {
		f.Platform = platform
	}
}

func WithHTTPClient(httpClient *http.Client) FindOption {
	return func(f *finder) {
		f.HTTPClient = httpClient
	}
}

// WithTagNamesCache sets the LRU cache for tag names.
func WithTagNamesCache(lru *expirable.LRU[string, []string]) FindOption {
	return func(f *finder) {
		f.tagNamesCache = lru
	}
}

// WithTagCreatedAtCache sets the LRU cache for tag created at times.
func WithTagCreatedAtCache(lru *expirable.LRU[string, time.Time]) FindOption {
	return func(f *finder) {
		f.tagCreatedAtCache = lru
	}
}

func WithListRemoteTags(fn func(context.Context, name.Repository) ([]string, error)) FindOption {
	return func(f *finder) { f.listRemoteTags = fn }
}

func WithResolveTagCreatedAt(fn func(context.Context, name.Repository, string, string, string) (time.Time, error)) FindOption {
	return func(f *finder) { f.resolveTagCreatedAt = fn }
}

func NewFinder(options ...FindOption) Finder {
	finderInstance := &finder{}
	for _, option := range options {
		option(finderInstance)
	}

	if finderInstance.tagNamesCache == nil {
		finderInstance.tagNamesCache = expirable.NewLRU[string, []string](defaultTagCacheMax, nil, defaultTagCacheTTL)
	}
	if finderInstance.tagCreatedAtCache == nil {
		finderInstance.tagCreatedAtCache = expirable.NewLRU[string, time.Time](defaultTagCacheMax, nil, defaultTagCacheTTL)
	}
	if finderInstance.listRemoteTags == nil {
		finderInstance.listRemoteTags = defaultListRemoteTags
	}
	if finderInstance.resolveTagCreatedAt == nil {
		finderInstance.resolveTagCreatedAt = resolveCreatedAt
	}

	return finderInstance
}

func (f *finder) platform() (os, arch string) {
	p := f.Platform
	if p == "" {
		p = "linux/amd64"
	}
	if i := strings.IndexByte(p, '/'); i > 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

func (f *finder) httpClient() *http.Client {
	if f.HTTPClient != nil {
		return f.HTTPClient
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}
