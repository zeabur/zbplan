package builder

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/tonistiigi/fsutil"
	"github.com/zeabur/zbplan/pkg/buildenv"
	"golang.org/x/sync/errgroup"

	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
)

type BuildImageOptions struct {
	Dockerfile string
	Context    string
	// AllowedRegistries lists the only image registries the build may pull
	// from. Empty uses registryutil's default allowlist.
	AllowedRegistries []string
	// ExcludeContextPath, when set, hides matching build-context paths from
	// BuildKit. Paths are slash-separated and relative to Context; excluding
	// a directory excludes its contents.
	ExcludeContextPath func(path string, isDir bool) bool
	Variables          map[string]string
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
	if err := validateDockerfile(options.Dockerfile); err != nil {
		return fmt.Errorf("validate dockerfile: %w", err)
	}
	policy, err := sourcePolicy(options.AllowedRegistries)
	if err != nil {
		return fmt.Errorf("build source policy: %w", err)
	}
	b.logger.InfoContext(ctx, "preparing build environment")
	prepared, err := buildenv.Prepare(ctx, options.Dockerfile, options.Variables)
	if err != nil {
		return fmt.Errorf("prepare build environment: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
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
	contextFS = newFilteredFS(contextFS, options.ExcludeContextPath)

	b.logger.InfoContext(ctx, "🐳 Writing Dockerfile...")
	if err = os.WriteFile(path.Join(tempDir, "Dockerfile"), []byte(prepared.Dockerfile), 0o644); err != nil {
		b.logger.ErrorContext(ctx, "Failed to write dockerfile", slog.Any("error", err))
		return fmt.Errorf("write dockerfile: %w", err)
	}

	dockerfileFS, err := fsutil.NewFS(tempDir)
	if err != nil {
		b.logger.ErrorContext(ctx, "Failed to create dockerfile filesystem", slog.Any("error", err))
		return fmt.Errorf("create dockerfile filesystem: %w", err)
	}

	frontendAttrs := prepared.FrontendAttrs()
	frontendAttrs["cmdline"] = bundledDockerfileFrontend
	// AllowedEntitlements stays empty: BuildKit then rejects RUN
	// --network=host and --security=insecure itself.
	solveOpt := client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			"context":    contextFS,
			"dockerfile": dockerfileFS,
		},
		Frontend:      "dockerfile.v0",
		FrontendAttrs: frontendAttrs,
		Session:       prepared.Session,
		Exports:       exports,
		SourcePolicy:  policy,
	}

	b.logger.InfoContext(ctx, "🚢 Building image...")

	ch := make(chan *client.SolveStatus, 1)
	egrp, ctx := errgroup.WithContext(ctx)

	egrp.Go(func() error {
		_, err := b.buildkitClient.Solve(ctx, nil, solveOpt, ch)
		return err
	})

	egrp.Go(func() error {
		// BuildKit's client blocks on every status send and closes ch only
		// when Solve returns, so this consumer must drain ch to its close.
		// Stopping on cancellation would deadlock a timed-out Solve.
		defer func() {
			for range ch {
			}
		}()
		output := NewSlogWriter(b.logger, "buildkit progress")
		d, err := progressui.NewDisplay(output, progressui.AutoMode)
		if err != nil {
			return err
		}
		_, err = d.UpdateFrom(context.WithoutCancel(ctx), ch)
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
// to w. w must be an io.WriteCloser because BuildKit's session layer
// (session/filesync.DiffCopy) calls Close() for stream finalization and
// propagates its error into the solve result. w is closed before BuildOCI
// returns.
func (b *builder) BuildOCI(ctx context.Context, options BuildImageOptions, w io.WriteCloser) error {
	if w == nil {
		return fmt.Errorf("nil output writer")
	}
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
