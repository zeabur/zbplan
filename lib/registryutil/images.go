package registryutil

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
)

type Image struct {
	Registry    string
	Name        string
	Description string
}

// Images searches for image repositories on a registry.
//
//   - registry "docker.io": searches via Docker Hub API.
//   - registry "ghcr.io": searches GitHub repos then filters to those with a real ghcr.io package.
func (f *finder) Images(ctx context.Context, registry, keyword string, limit int) ([]Image, error) {
	if registry == "" {
		registry = RegistryDockerHub
	}
	switch registry {
	case RegistryDockerHub:
		return f.searchDockerHub(ctx, keyword, limit)
	case RegistryGHCR:
		return f.searchGHCR(ctx, keyword, limit)
	default:
		return nil, fmt.Errorf("image search not supported for registry %q", registry)
	}
}

func (f *finder) searchDockerHub(ctx context.Context, keyword string, limit int) ([]Image, error) {
	u := "https://hub.docker.com/v2/search/repositories/?query=" +
		url.QueryEscape(keyword) + "&page_size=" + strconv.Itoa(limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub search returned %s", resp.Status)
	}

	var body struct {
		Results []struct {
			RepoName         string `json:"repo_name"`
			ShortDescription string `json:"short_description"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding hub search response: %w", err)
	}

	images := make([]Image, 0, len(body.Results))
	for _, r := range body.Results {
		images = append(images, Image{
			Registry:    RegistryDockerHub,
			Name:        r.RepoName,
			Description: r.ShortDescription,
		})
	}
	return images, nil
}

func (f *finder) searchGHCR(ctx context.Context, keyword string, limit int) ([]Image, error) {
	u := "https://api.github.com/search/repositories?q=" +
		url.QueryEscape(keyword) + "&per_page=" + strconv.Itoa(limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github search returned %s", resp.Status)
	}

	var body struct {
		Items []struct {
			FullName    string `json:"full_name"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding github search response: %w", err)
	}

	imageChan := make(chan Image, len(body.Items))

	go func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var wg sync.WaitGroup
		for _, item := range body.Items {
			wg.Go(func() {
				if ctx.Err() != nil {
					return
				}

				if f.hasGHCRPackage(ctx, item.FullName) {
					imageChan <- Image{
						Registry:    RegistryGHCR,
						Name:        item.FullName,
						Description: item.Description,
					}
				}
			})
		}
		wg.Wait()
		close(imageChan)
	}()

	var images []Image
	for image := range imageChan {
		images = append(images, image)
	}

	return images, nil
}

// hasGHCRPackage reports whether fullName (e.g. "astral-sh/uv") has a public
// package on ghcr.io by requesting an anonymous pull token. The token endpoint
// returns 403 when the package does not exist.
func (f *finder) hasGHCRPackage(ctx context.Context, fullName string) bool {
	scope := "repository:" + fullName + ":pull"
	u := "https://ghcr.io/token?service=ghcr.io&scope=" + url.QueryEscape(scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := f.httpClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.Token != ""
}
