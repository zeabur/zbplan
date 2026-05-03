package plantools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/moby/buildkit/client"
	slogmulti "github.com/samber/slog-multi"
	"github.com/zeabur/zbplan/lib/builder"
	"github.com/zendev-sh/goai"
)

type BuilderBaseContext struct {
	BuildkitAddr string

	ContextDir string
	Variables  map[string]string
}

func NewDockerfileCanBuildTool(ctx context.Context, builderContext BuilderBaseContext) (goai.Tool, error) {
	type Args struct {
		Dockerfile string `json:"dockerfile"`
	}

	buildkitClient, err := client.New(ctx, builderContext.BuildkitAddr)
	if err != nil {
		return goai.Tool{}, fmt.Errorf("new buildkit client: %w", err)
	}

	return goai.Tool{
		Name:        "build_and_check_dockerfile",
		Description: "Build and check if the Dockerfile can be built for the given Dockerfile string.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"dockerfile": {
					"type":        "string",
					"description": "The Dockerfile attempting to build."
				}
			},
			"required": ["dockerfile"]
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			buildLogs := &bytes.Buffer{}
			logger := slog.New(slogmulti.Fanout(
				slog.Default().Handler(),
				slog.NewTextHandler(buildLogs, nil),
			))

			buildkitBuilder := builder.NewBuildkitBuilder(buildkitClient, logger)

			var args Args
			err := json.Unmarshal(input, &args)
			if err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
			}
			if args.Dockerfile == "" {
				return "", fmt.Errorf("dockerfile is required")
			}

			result, err := buildkitBuilder.BuildOCI(ctx, builder.BuildImageOptions{
				Dockerfile: args.Dockerfile,
				Context:    builderContext.ContextDir,
				Variables:  builderContext.Variables,
			})
			if err != nil {
				return "", fmt.Errorf("cannot build: %w\nresult:\n%s", err, buildLogs.String())
			}
			buildLogs.Reset()

			err = os.Remove(result.TarballPath)
			if err != nil {
				slog.WarnContext(ctx, "failed to clean up tarball", "path", result.TarballPath, "error", err)
				return "", fmt.Errorf("cannot remove tarball: %w", err)
			}

			return "build succeeded", nil
		},
	}, nil
}
