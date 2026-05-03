package builder

import (
	"context"
	"fmt"
	"io"
	"log"
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

type buildImageOptions struct {
	buildkitAddress string
	output          io.Writer
	variables       map[string]string
}

type BuildImageOptionsFn func(opts *buildImageOptions)

func WithBuildKitAddress(address string) BuildImageOptionsFn {
	return func(opts *buildImageOptions) {
		opts.buildkitAddress = address
	}
}

func WithBuildOutput(output io.Writer) BuildImageOptionsFn {
	return func(opts *buildImageOptions) {
		opts.output = output
	}
}

func WithVariables(variables map[string]string) BuildImageOptionsFn {
	return func(opts *buildImageOptions) {
		opts.variables = variables
	}
}

// BuildImage builds a container image using BuildKit and saves it as an OCI tarball.
// It returns the path to the generated tarball.
//
// dockerfile is the content of Dockerfile. context is the context directory that
// Dockerfile will build on.
func BuildImage(ctx context.Context, dockerfile string, context string, opts ...BuildImageOptionsFn) (string, error) {
	opt := &buildImageOptions{
		output:    os.Stderr,
		variables: map[string]string{},
	}
	for _, fn := range opts {
		fn(opt)
	}

	processor := NewPipelineProcessor()
	if len(opt.variables) > 0 {
		keys := slices.Sorted(maps.Keys(opt.variables))
		processor.Processors = append(processor.Processors, &EnvProcessor{Variables: keys})
	}

	_, _ = fmt.Fprintf(opt.output, "🔧 Pre-processing Dockerfile...\n")
	dockerfile, err := processor.Process(ctx, dockerfile)
	if err != nil {
		return "", fmt.Errorf("pre-process dockerfile: %w", err)
	}

	// Create a temporary directory for the output
	tempDir, err := os.MkdirTemp("", "zbpack-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	outputPath := path.Join(tempDir, "image.tar")

	// Initialize BuildKit client
	_, _ = fmt.Fprintf(opt.output, "🔌 Initializing build environment...\n")
	c, err := client.New(ctx, opt.buildkitAddress)
	if err != nil {
		return "", fmt.Errorf("create buildkit client: %w", err)
	}

	// Create output file
	outputFile, err := os.Create(outputPath)
	if err != nil {
		return "", fmt.Errorf("create output file: %w", err)
	}
	defer func() {
		_ = outputFile.Close()
	}()

	// Create context and dockerfile filesystem access
	contextFS, err := fsutil.NewFS(context)
	if err != nil {
		return "", fmt.Errorf("create context filesystem: %w", err)
	}

	err = os.WriteFile(path.Join(tempDir, "Dockerfile"), []byte(dockerfile), 0o644)
	if err != nil {
		return "", fmt.Errorf("write dockerfile: %w", err)
	}

	dockerfileFS, err := fsutil.NewFS(tempDir)
	if err != nil {
		return "", fmt.Errorf("create dockerfile filesystem: %w", err)
	}

	frontendAttrs := make(map[string]string, len(opt.variables)+1)
	frontendAttrs["filename"] = "Dockerfile"
	for k, v := range opt.variables {
		frontendAttrs["build-arg:ZEABUR_ENV_"+EncodeArgName(k)] = v
	}

	// Set up solve options
	solveOpt := client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			"context":    contextFS,
			"dockerfile": dockerfileFS,
		},
		Frontend:      "dockerfile.v0",
		FrontendAttrs: frontendAttrs,
		Exports: []client.ExportEntry{
			{
				Type: client.ExporterOCI,
				Output: func(_ map[string]string) (io.WriteCloser, error) {
					return outputFile, nil
				},
			},
		},
	}

	_, _ = fmt.Fprintf(opt.output, "⏳ Building image...\n")

	// Set up progress display
	ch := make(chan *client.SolveStatus, 1)
	egrp, ctx := errgroup.WithContext(ctx)

	egrp.Go(func() error {
		_, err := c.Solve(ctx, nil, solveOpt, ch)
		return err
	})

	egrp.Go(func() error {
		d, err := progressui.NewDisplay(opt.output, progressui.AutoMode)
		if err != nil {
			return err
		}
		_, err = d.UpdateFrom(ctx, ch)
		return err
	})

	// Wait for build to complete
	if err := egrp.Wait(); err != nil {
		return "", fmt.Errorf("build failed: %w", err)
	}

	log.Println("Built image: ", outputPath)
	_, _ = fmt.Fprintf(opt.output, "📦 Build completed. Ready to push.\n")

	return outputPath, nil
}
