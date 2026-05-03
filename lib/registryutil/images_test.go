package registryutil_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zeabur/zbplan/lib/registryutil"
)

// mockTransport serves requests through an http.Handler without a real TCP connection.
type mockTransport struct{ h http.Handler }

func (m *mockTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	m.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func newMockFinder(h http.Handler) registryutil.Finder {
	return registryutil.NewFinder(registryutil.WithHTTPClient(&http.Client{
		Transport: &mockTransport{h},
	}))
}

// routeByHost dispatches to per-host handlers based on r.URL.Host.
type routeByHost map[string]http.Handler

func (r routeByHost) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if h, ok := r[req.URL.Host]; ok {
		h.ServeHTTP(w, req)
		return
	}
	http.NotFound(w, req)
}

func jsonHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

// Fixtures captured from real APIs on 2026-05-03.

const dockerHubNginxJSON = `{"count":284295,"next":"https://hub.docker.com/v2/search/repositories/?page=2&page_size=3&query=nginx","previous":"","results":[{"repo_name":"nginx","short_description":"Official build of Nginx.","star_count":21268,"pull_count":12981954060,"repo_owner":"","is_automated":false,"is_official":true},{"repo_name":"nginx/nginx-ingress","short_description":"NGINX and  NGINX Plus Ingress Controllers for Kubernetes","star_count":120,"pull_count":1085123768,"repo_owner":"","is_automated":false,"is_official":false},{"repo_name":"nginx/nginx-prometheus-exporter","short_description":"NGINX Prometheus Exporter for NGINX and NGINX Plus","star_count":51,"pull_count":87347846,"repo_owner":"","is_automated":false,"is_official":false}]}`

// ghcrFilterJSON contains astral-sh/uv (has a ghcr.io image) and zeabur/zeabur (no ghcr.io image).
const ghcrFilterJSON = `{"total_count":2,"incomplete_results":false,"items":[{"full_name":"astral-sh/uv","description":"An extremely fast Python package and project manager, written in Rust."},{"full_name":"zeabur/zeabur","description":"Zeabur"}]}`

// ghcrTokenHandler grants a token for astral-sh/uv and returns 403 for everything else,
// mirroring real ghcr.io behaviour (403 = package does not exist).
func ghcrTokenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("scope"), "astral-sh/uv") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"dummy"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})
}

// Images — Docker Hub

func TestImages_DockerHub_ReturnsResults(t *testing.T) {
	t.Parallel()
	f := newMockFinder(jsonHandler(dockerHubNginxJSON))

	images, err := f.Images(context.Background(), registryutil.RegistryDockerHub, "nginx", 3)
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if len(images) == 0 {
		t.Fatal("expected at least one image, got none")
	}
	for _, img := range images {
		if img.Registry != registryutil.RegistryDockerHub {
			t.Errorf("expected Registry=%q, got %q", registryutil.RegistryDockerHub, img.Registry)
		}
		if img.Name == "" {
			t.Error("image has empty Name")
		}
	}
}

func TestImages_DockerHub_LimitRespected(t *testing.T) {
	t.Parallel()
	f := newMockFinder(jsonHandler(dockerHubNginxJSON))

	images, err := f.Images(context.Background(), registryutil.RegistryDockerHub, "nginx", 3)
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if len(images) > 3 {
		t.Errorf("expected at most 3 images, got %d", len(images))
	}
}

func TestImages_DockerHub_ServerError(t *testing.T) {
	t.Parallel()
	f := newMockFinder(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	_, err := f.Images(context.Background(), registryutil.RegistryDockerHub, "nginx", 3)
	if err == nil {
		t.Fatal("expected error on server error, got nil")
	}
}

// Images — ghcr.io

func TestImages_GHCR_FiltersReposWithoutPackage(t *testing.T) {
	t.Parallel()
	f := newMockFinder(routeByHost{
		"api.github.com": jsonHandler(ghcrFilterJSON),
		"ghcr.io":        ghcrTokenHandler(),
	})

	images, err := f.Images(context.Background(), registryutil.RegistryGHCR, "uv", 5)
	if err != nil {
		t.Fatalf("Images(ghcr.io): %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("expected exactly 1 image (astral-sh/uv), got %d", len(images))
	}
	if images[0].Name != "astral-sh/uv" {
		t.Errorf("expected Name=%q, got %q", "astral-sh/uv", images[0].Name)
	}
	if images[0].Registry != registryutil.RegistryGHCR {
		t.Errorf("expected Registry=%q, got %q", registryutil.RegistryGHCR, images[0].Registry)
	}
}

func TestImages_GHCR_LimitRespected(t *testing.T) {
	t.Parallel()
	f := newMockFinder(routeByHost{
		"api.github.com": jsonHandler(ghcrFilterJSON),
		"ghcr.io":        ghcrTokenHandler(),
	})

	images, err := f.Images(context.Background(), registryutil.RegistryGHCR, "uv", 2)
	if err != nil {
		t.Fatalf("Images(ghcr.io): %v", err)
	}
	if len(images) > 2 {
		t.Errorf("expected at most 2 images, got %d", len(images))
	}
}

func TestImages_GHCR_ServerError(t *testing.T) {
	t.Parallel()
	f := newMockFinder(routeByHost{
		"api.github.com": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}),
	})

	_, err := f.Images(context.Background(), registryutil.RegistryGHCR, "runner", 3)
	if err == nil {
		t.Fatal("expected error on GitHub server error, got nil")
	}
}

// Images — unsupported registry

func TestImages_UnsupportedRegistry_ReturnsError(t *testing.T) {
	t.Parallel()
	f := newMockFinder(http.NotFoundHandler())

	_, err := f.Images(context.Background(), "quay.io", "nginx", 5)
	if err == nil {
		t.Fatal("expected error for unsupported registry, got nil")
	}
}
