package plantools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/distribution/reference"
	"github.com/zeabur/zbplan/pkg/registryutil"
)

type listImagesTool struct {
	finder     registryutil.Finder
	registries []string
}

// NewListImagesTool searches only the given registries. Pass
// registryutil.SearchableRegistries of the run's allowlist so the agent never
// contacts, or is offered images from, a registry the build would deny.
func NewListImagesTool(registries []string) tool.InvokableTool {
	return &listImagesTool{
		finder:     registryutil.NewFinder(),
		registries: slices.Clone(registries),
	}
}

func (t *listImagesTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_images",
		Desc: "Searches for Docker images matching the query on " + strings.Join(t.registries, " and ") + ". Use this to find candidate base images.",
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
	if len(args.Query) > 128 {
		return "", fmt.Errorf("query is too long")
	}
	result, err := ListImages(ctx, t.finder, t.registries, args.Query)
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
	finder            registryutil.Finder
	allowedRegistries []string
}

func NewListTagsTool(allowedRegistries []string) tool.InvokableTool {
	if normalized, err := registryutil.NormalizeAllowedRegistries(allowedRegistries); err == nil {
		allowedRegistries = normalized
	}
	return &listTagsTool{
		finder:            registryutil.NewFinder(),
		allowedRegistries: slices.Clone(allowedRegistries),
	}
}

func (t *listTagsTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_tags",
		Desc: "Lists tags for a specific Docker image on a given registry. Use this after finding a candidate image with list_images.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"registry": {
				Type:     schema.String,
				Desc:     "The registry hosting the image; one of: " + strings.Join(t.allowedRegistries, ", "),
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
	result, err := ListTags(ctx, t.finder, t.allowedRegistries, args.Registry, args.Image, args.Query)
	if err != nil {
		return "", fmt.Errorf("list tags: %w", err)
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(out), nil
}

// ListImages searches registries concurrently. Per-registry failures are
// logged and omitted so one unavailable registry does not hide the others.
func ListImages(ctx context.Context, finder registryutil.Finder, registries []string, query string) ([]registryutil.Image, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if len(query) > 128 {
		return nil, fmt.Errorf("query is too long")
	}

	const maxPerRegistry = 3

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

func ListTags(
	ctx context.Context,
	finder registryutil.Finder,
	allowedRegistries []string,
	registry, image, query string,
) ([]registryutil.Tag, error) {
	const maxTags = 5

	registry = strings.ToLower(strings.TrimSpace(registry))
	normalized, err := registryutil.NormalizeAllowedRegistries(allowedRegistries)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(normalized, registry) {
		return nil, fmt.Errorf("registry %q is not allowed", registry)
	}
	// The registry is fixed by the allowlist; the image must be a valid
	// repository path under it per the distribution reference grammar.
	if len(image) > 255 {
		return nil, fmt.Errorf("image is too long")
	}
	named, err := reference.ParseNormalizedNamed(registry + "/" + image)
	if err != nil || !reference.IsNameOnly(named) || reference.Domain(named) != registry {
		return nil, fmt.Errorf("image must be a lowercase repository path")
	}
	if len(query) > 128 {
		return nil, fmt.Errorf("query is too long")
	}

	if query == "" {
		query = "latest"
	}

	return finder.Tags(ctx, registry, image, query, maxTags)
}
