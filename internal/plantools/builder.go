package plantools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/moby/buildkit/client"
	slogmulti "github.com/samber/slog-multi"
	"github.com/zeabur/zbplan/pkg/builder"
)

// BuilderClient wraps a BuildKit client and build context for repeated builds.
type BuilderClient struct {
	contextDir string
	variables  map[string]string
	client     *client.Client
}

// NewBuilderClient dials BuildKit and returns a BuilderClient ready for builds.
func NewBuilderClient(ctx context.Context, addr, contextDir string, variables map[string]string) (*BuilderClient, error) {
	c, err := client.New(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("new buildkit client: %w", err)
	}
	return &BuilderClient{
		contextDir: contextDir,
		variables:  variables,
		client:     c,
	}, nil
}

// Close releases the underlying BuildKit connection.
func (b *BuilderClient) Close() error {
	return b.client.Close()
}

// RunBuildOCI builds the given Dockerfile and streams the resulting OCI tarball to w.
func (b *BuilderClient) RunBuildOCI(ctx context.Context, dockerfile string, w io.WriteCloser) error {
	logBuf := &bytes.Buffer{}
	logger := slog.New(slogmulti.Fanout(
		slog.Default().Handler(),
		slog.NewTextHandler(logBuf, nil),
	))

	bld := builder.NewBuildkitBuilder(b.client, logger)
	if err := bld.BuildOCI(ctx, builder.BuildImageOptions{
		Dockerfile: dockerfile,
		Context:    b.contextDir,
		Variables:  b.variables,
	}, w); err != nil {
		return fmt.Errorf("build oci: %w", err)
	}
	return nil
}

// RunBuild tries to build the given Dockerfile.
// On failure it returns the captured build logs alongside the error.
// On success it returns empty logs and nil error.
func (b *BuilderClient) RunBuild(ctx context.Context, dockerfile string) (buildLogs string, err error) {
	logBuf := &bytes.Buffer{}
	logger := slog.New(slogmulti.Fanout(
		slog.Default().Handler(),
		slog.NewTextHandler(logBuf, nil),
	))

	bld := builder.NewBuildkitBuilder(b.client, logger)
	if err := bld.Build(ctx, builder.BuildImageOptions{
		Dockerfile: dockerfile,
		Context:    b.contextDir,
		Variables:  b.variables,
	}); err != nil {
		return logBuf.String(), fmt.Errorf("build failed: %w", err)
	}
	return "", nil
}
