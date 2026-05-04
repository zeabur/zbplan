package registryutil

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const (
	RegistryDockerHub = "docker.io"
	RegistryGHCR      = "ghcr.io"
)

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

func NewFinder(options ...FindOption) *finder {
	finderInstance := &finder{}
	for _, option := range options {
		option(finderInstance)
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
	return http.DefaultClient
}
