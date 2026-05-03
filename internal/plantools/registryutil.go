package plantools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/zeabur/zbplan/lib/registryutil"
	"github.com/zendev-sh/goai"
)

func NewListImagesTool() goai.Tool {
	type Args struct {
		Query string `json:"query"`
	}

	finder := registryutil.NewFinder()

	return goai.Tool{
		Name:        "list_images",
		Description: "Searches for Docker images matching the query on docker.io and ghcr.io. Use this to find candidate base images.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type":        "string",
					"description": "The search query for the image, e.g. 'go', 'python', 'node', 'bun'"
				}
			},
			"required": ["query"]
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args Args
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
			}
			if args.Query == "" {
				return "", fmt.Errorf("query is required")
			}

			result, err := ListImages(ctx, finder, args.Query)
			if err != nil {
				return "", fmt.Errorf("list images: %w", err)
			}

			out, err := json.Marshal(result)
			if err != nil {
				return "", fmt.Errorf("marshal result: %w", err)
			}
			return string(out), nil
		},
	}
}

func NewListTagsTool() goai.Tool {
	type Args struct {
		Registry string `json:"registry"`
		Image    string `json:"image"`
		Query    string `json:"query,omitempty"`
	}

	finder := registryutil.NewFinder()

	return goai.Tool{
		Name:        "list_tags",
		Description: "Lists tags for a specific Docker image on a given registry. Use this after finding a candidate image with list_images.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"registry": {
					"type":        "string",
					"description": "The registry hosting the image, e.g. 'docker.io', 'ghcr.io'"
				},
				"image": {
					"type":        "string",
					"description": "The image name, e.g. 'golang', 'python', 'library/node'"
				},
				"query": {
					"type":        "string",
					"description": "Tag filter/search query, e.g. 'latest', '1.26', '22'. Defaults to 'latest' if empty."
				}
			},
			"required": ["registry", "image"]
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args Args
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
			}
			if args.Registry == "" {
				return "", fmt.Errorf("registry is required")
			}
			if args.Image == "" {
				return "", fmt.Errorf("image is required")
			}

			result, err := ListTags(ctx, finder, args.Registry, args.Image, args.Query)
			if err != nil {
				return "", fmt.Errorf("list tags: %w", err)
			}

			out, err := json.Marshal(result)
			if err != nil {
				return "", fmt.Errorf("marshal result: %w", err)
			}
			return string(out), nil
		},
	}
}

func ListImages(ctx context.Context, finder registryutil.Finder, query string) ([]registryutil.Image, error) {
	const maxPerRegistry = 3

	registries := []string{"docker.io", "ghcr.io"}
	resultChan := make(chan registryutil.Image, maxPerRegistry*len(registries))

	go func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var wg sync.WaitGroup
		for _, registry := range registries {
			wg.Go(func() {
				if ctx.Err() != nil {
					return
				}
				images, err := finder.Images(ctx, registry, query, maxPerRegistry)
				if err != nil {
					slog.Error("failed to search images", "registry", registry, "error", err)
					return
				}
				for _, img := range images {
					resultChan <- img
				}
			})
		}
		wg.Wait()
		close(resultChan)
	}()

	results := make([]registryutil.Image, 0, maxPerRegistry*len(registries))
	for img := range resultChan {
		results = append(results, img)
	}
	return results, nil
}

func ListTags(ctx context.Context, finder registryutil.Finder, registry, image, query string) ([]registryutil.Tag, error) {
	const maxTags = 5

	if query == "" {
		query = "latest"
	}

	return finder.Tags(ctx, registry, image, query, maxTags)
}
