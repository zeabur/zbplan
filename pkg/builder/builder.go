package builder

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path"
	"slices"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"

	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
)

type BuildImageOptions struct {
	Dockerfile string
	Context    string
	Variables  map[string]string
}

type Builder interface {
	Build(ctx context.Context, options BuildImageOptions) error
	BuildOCI(ctx context.Context, options BuildImageOptions, w io.WriteCloser) error
}

type builder struct {
	buildkitClient *client.Client
	logger         *slog.Logger
}

func NewBuildkitBuilder(buildkitClient *client.Client, logger *slog.Logger) *builder {
	return &builder{
		buildkitClient: buildkitClient,
		logger:         logger,
	}
}

func (b *builder) solve(ctx context.Context, options BuildImageOptions, exports []client.ExportEntry) error {
	processor := NewPipelineProcessor()
	if len(options.Variables) > 0 {
		keys := slices.Sorted(maps.Keys(options.Variables))
		processor.Processors = append(processor.Processors, &EnvProcessor{Variables: keys})
	}

	b.logger.InfoContext(ctx, "🔧 Pre-processing Dockerfile...")
	dockerfile, err := processor.Process(ctx, options.Dockerfile)
	if err != nil {
		b.logger.ErrorContext(ctx, "Failed to pre-process dockerfile", slog.Any("error", err))
		return fmt.Errorf("pre-process dockerfile: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "zbpack-")
	if err != nil {
		b.logger.ErrorContext(ctx, "Failed to create temp dir", slog.Any("error", err))
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	contextFS, err := fsutil.NewFS(options.Context)
	if err != nil {
		b.logger.ErrorContext(ctx, "Failed to create context filesystem", slog.Any("error", err))
		return fmt.Errorf("create context filesystem: %w", err)
	}

	b.logger.InfoContext(ctx, "🐳 Writing Dockerfile...")
	if err = os.WriteFile(path.Join(tempDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		b.logger.ErrorContext(ctx, "Failed to write dockerfile", slog.Any("error", err))
		return fmt.Errorf("write dockerfile: %w", err)
	}

	dockerfileFS, err := fsutil.NewFS(tempDir)
	if err != nil {
		b.logger.ErrorContext(ctx, "Failed to create dockerfile filesystem", slog.Any("error", err))
		return fmt.Errorf("create dockerfile filesystem: %w", err)
	}

	frontendAttrs := make(map[string]string, len(options.Variables)+1)
	frontendAttrs["filename"] = "Dockerfile"
	for k, v := range options.Variables {
		frontendAttrs["build-arg:ZEABUR_ENV_"+EncodeArgName(k)] = v
	}

	solveOpt := client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			"context":    contextFS,
			"dockerfile": dockerfileFS,
		},
		Frontend:      "dockerfile.v0",
		FrontendAttrs: frontendAttrs,
		Exports:       exports,
	}

	b.logger.InfoContext(ctx, "🚢 Building image...")

	ch := make(chan *client.SolveStatus, 1)
	egrp, ctx := errgroup.WithContext(ctx)

	egrp.Go(func() error {
		_, err := b.buildkitClient.Solve(ctx, nil, solveOpt, ch)
		return err
	})

	egrp.Go(func() error {
		output := NewSlogWriter(b.logger, "buildkit progress")
		d, err := progressui.NewDisplay(output, progressui.AutoMode)
		if err != nil {
			return err
		}
		_, err = d.UpdateFrom(ctx, ch)
		return err
	})

	if err := egrp.Wait(); err != nil {
		b.logger.ErrorContext(ctx, "Build failed", slog.Any("error", err))
		return fmt.Errorf("build failed: %w", err)
	}

	return nil
}

// Build runs a BuildKit solve with no exporter. Use this to verify that a
// Dockerfile builds successfully without producing any artifact.
func (b *builder) Build(ctx context.Context, options BuildImageOptions) error {
	if err := b.solve(ctx, options, nil); err != nil {
		return err
	}
	b.logger.InfoContext(ctx, "📦 Build completed.")
	return nil
}

// BuildOCI builds a container image using BuildKit and streams the OCI tarball
// to w.
func (b *builder) BuildOCI(ctx context.Context, options BuildImageOptions, w io.WriteCloser) error {
	exports := []client.ExportEntry{
		{
			Type: client.ExporterOCI,
			Output: func(_ map[string]string) (io.WriteCloser, error) {
				return w, nil
			},
		},
	}

	if err := b.solve(ctx, options, exports); err != nil {
		return err
	}

	b.logger.InfoContext(ctx, "📦 Build completed.")
	return nil
}
