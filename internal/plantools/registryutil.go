package plantools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/zeabur/zbplan/lib/registryutil"
)

type listImagesTool struct {
	finder registryutil.Finder
}

func NewListImagesTool() tool.InvokableTool {
	return &listImagesTool{finder: registryutil.NewFinder()}
}

func (t *listImagesTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_images",
		Desc: "Searches for Docker images matching the query on docker.io and ghcr.io. Use this to find candidate base images.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "The search query for the image, e.g. 'go', 'python', 'node', 'bun'",
				Required: true,
			},
		}),
	}, nil
}

func (t *listImagesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	result, err := ListImages(ctx, t.finder, args.Query)
	if err != nil {
		return "", fmt.Errorf("list images: %w", err)
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

type listTagsTool struct {
	finder registryutil.Finder
}

func NewListTagsTool() tool.InvokableTool {
	return &listTagsTool{finder: registryutil.NewFinder()}
}

func (t *listTagsTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_tags",
		Desc: "Lists tags for a specific Docker image on a given registry. Use this after finding a candidate image with list_images.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"registry": {
				Type:     schema.String,
				Desc:     "The registry hosting the image, e.g. 'docker.io', 'ghcr.io'",
				Required: true,
			},
			"image": {
				Type:     schema.String,
				Desc:     "The image name, e.g. 'golang', 'python', 'library/node'",
				Required: true,
			},
			"query": {
				Type: schema.String,
				Desc: "Tag filter/search query, e.g. 'latest', '1.26', '22'. Defaults to 'latest' if empty.",
			},
		}),
	}, nil
}

func (t *listTagsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Registry string `json:"registry"`
		Image    string `json:"image"`
		Query    string `json:"query,omitempty"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Registry == "" {
		return "", fmt.Errorf("registry is required")
	}
	if args.Image == "" {
		return "", fmt.Errorf("image is required")
	}
	result, err := ListTags(ctx, t.finder, args.Registry, args.Image, args.Query)
	if err != nil {
		return "", fmt.Errorf("list tags: %w", err)
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
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
