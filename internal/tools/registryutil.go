package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/zeabur/zbplan/lib/registryutil"
	"github.com/zendev-sh/goai"
)

func NewFindImageTool() goai.Tool {
	type Args struct {
		Query   string `json:"query"`
		Version string `json:"version,omitempty"`
	}

	finder := registryutil.NewFinder()

	return goai.Tool{
		Name:        "find_docker_image",
		Description: "Finds an image available for the given toolchain. Use it for searching the base images. If there is no recommended one, we search it on Docker Hub and ghcr.io.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type":        "string",
					"description": "The search query for the image, e.g. 'go', 'python', 'node', 'bun'",
				},
				"version": {
					"type":        "string",
					"description": "The version of the image to find, e.g. 'latest', '1.26', '22'. If none is specified, we return the latest tag.",
				}
			},
			"required": ["query"],
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args Args
			err := json.Unmarshal(input, &args)
			if err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
			}
			if args.Query == "" {
				return "", fmt.Errorf("query is required")
			}

			result, err := FindImage(ctx, finder, args.Query, args.Version)
			if err != nil {
				return "", fmt.Errorf("find imge: %w", err)
			}

			marshalledResult, err := json.Marshal(result)
			if err != nil {
				return "", fmt.Errorf("marshal result: %w", err)
			}

			return string(marshalledResult), nil
		},
	}
}

type FindImageResultEntry struct {
	Registry    string             `json:"registry"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Tags        []registryutil.Tag `json:"tags"`
}

func FindImage(ctx context.Context, finder registryutil.Finder, query, version string) ([]FindImageResultEntry, error) {
	const maxSearchResultsForEachRegistry = 10
	registries := []string{"docker.io", "ghcr.io"}

	if version == "" {
		version = "latest"
	}

	resultChan := make(chan FindImageResultEntry, maxSearchResultsForEachRegistry*len(registries))
	results := make([]FindImageResultEntry, 0, maxSearchResultsForEachRegistry*len(registries))

	go func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var wg sync.WaitGroup

		for _, registry := range registries {
			wg.Go(func() {
				if ctx.Err() != nil {
					return
				}

				imageCandidates, err := finder.Images(ctx, registry, query, maxSearchResultsForEachRegistry)
				if err != nil {
					slog.Error("failed to search images", "registry", registry, "error", err)
					return
				}

				if ctx.Err() != nil {
					return
				}

				var imageWg sync.WaitGroup
				for _, imageCandidate := range imageCandidates {
					imageWg.Go(func() {
						ctx, cancel := context.WithCancel(ctx)
						defer cancel()

						slog.Info("found image", "registry", imageCandidate.Registry, "name", imageCandidate.Name, "description", imageCandidate.Description)
						tags, err := finder.Tags(ctx, imageCandidate.Registry, imageCandidate.Name, "", 10)
						if err != nil {
							slog.Error("failed to find tags", "registry", imageCandidate.Registry, "name", imageCandidate.Name, "error", err)
							return
						}
						slog.Info("found tags", "registry", imageCandidate.Registry, "name", imageCandidate.Name, "tags", tags)

						resultChan <- FindImageResultEntry{
							Registry:    imageCandidate.Registry,
							Name:        imageCandidate.Name,
							Description: imageCandidate.Description,
							Tags:        tags,
						}
					})
				}
				imageWg.Wait()
			})
		}

		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		results = append(results, result)
	}

	return results, nil
}
