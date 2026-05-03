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

func NewListImagesAndTagsTool() goai.Tool {
	type Args struct {
		ImageQuery string `json:"image_query"`
		TagQuery   string `json:"tag_query,omitempty"`
	}

	finder := registryutil.NewFinder()

	return goai.Tool{
		Name:        "list_docker_images_and_tags",
		Description: "Lists available Docker images and their tags for the given query on docker.io and ghcr.io. Use it for searching the base images.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"image_query": {
					"type":        "string",
					"description": "The search query for the image, e.g. 'go', 'python', 'node', 'bun'"
				},
				"tag_query": {
					"type":        "string",
					"description": "The tag of the image to find, e.g. 'latest', '1.26', '22'. If none is specified, we return the latest tag."
				}
			},
			"required": ["image_query", "tag_query"]
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args Args
			err := json.Unmarshal(input, &args)
			if err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
			}
			if args.ImageQuery == "" {
				return "", fmt.Errorf("query is required")
			}

			result, err := ListImagesAndTags(ctx, finder, args.ImageQuery, args.TagQuery)
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

type ListImagesAndTagsEntry struct {
	Registry    string             `json:"registry"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Tags        []registryutil.Tag `json:"tags"`
}

func ListImagesAndTags(ctx context.Context, finder registryutil.Finder, imageQuery, tagQuery string) ([]ListImagesAndTagsEntry, error) {
	const maxSearchResultsForEachRegistry = 3
	const maxTagsForEachImage = 3

	registries := []string{"docker.io", "ghcr.io"}

	if tagQuery == "" {
		tagQuery = "latest"
	}

	resultChan := make(chan ListImagesAndTagsEntry, maxSearchResultsForEachRegistry*len(registries))
	results := make([]ListImagesAndTagsEntry, 0, maxSearchResultsForEachRegistry*len(registries))

	go func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var wg sync.WaitGroup

		for _, registry := range registries {
			wg.Go(func() {
				if ctx.Err() != nil {
					return
				}

				imageCandidates, err := finder.Images(ctx, registry, imageQuery, maxSearchResultsForEachRegistry)
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

						tags, err := finder.Tags(ctx, imageCandidate.Registry, imageCandidate.Name, tagQuery, maxTagsForEachImage)
						if err != nil {
							slog.Error("failed to find tags", "registry", imageCandidate.Registry, "name", imageCandidate.Name, "error", err)
							return
						}

						resultChan <- ListImagesAndTagsEntry{
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
