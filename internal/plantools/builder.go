package plantools

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/moby/buildkit/client"
	slogmulti "github.com/samber/slog-multi"
	"github.com/zeabur/zbplan/pkg/builder"
)

type BuilderClientConfig struct {
	Addr              string
	ContextDir        string
	AllowedRegistries []string
	Timeout           time.Duration
	MaxLogBytes       int
}

// BuilderClient wraps a BuildKit client and build context for repeated builds.
type BuilderClient struct {
	contextDir        string
	allowedRegistries []string
	timeout           time.Duration
	maxLogBytes       int
	client            *client.Client
	solveCount        atomic.Int64
}

// NewBuilderClient dials BuildKit and returns a BuilderClient ready for builds.
func NewBuilderClient(ctx context.Context, cfg BuilderClientConfig) (*BuilderClient, error) {
	c, err := client.New(ctx, cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("new buildkit client: %w", err)
	}
	return &BuilderClient{
		contextDir:        cfg.ContextDir,
		allowedRegistries: cfg.AllowedRegistries,
		timeout:           cfg.Timeout,
		maxLogBytes:       cfg.MaxLogBytes,
		client:            c,
	}, nil
}

// Close releases the underlying BuildKit connection.
func (b *BuilderClient) Close() error {
	return b.client.Close()
}

// BuildSolves returns the number of BuildKit Solve RPCs started by this client.
func (b *BuilderClient) BuildSolves() int {
	return int(b.solveCount.Load())
}

// RunBuild builds the Dockerfile once. When ociOutput is non-nil, the same
// solve exports the OCI artifact. BuildKit closes ociOutput during solve
// finalization.
func (b *BuilderClient) RunBuild(ctx context.Context, dockerfile string, ociOutput io.WriteCloser) (buildLogs string, err error) {
	if b.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.timeout)
		defer cancel()
	}

	logBuf := newBoundedLogBuffer(b.maxLogBytes)
	logger := slog.New(slogmulti.Fanout(
		slog.Default().Handler(),
		slog.NewTextHandler(logBuf, nil),
	))

	bld := builder.NewBuildkitBuilder(b.client, logger, func() { b.solveCount.Add(1) })
	options := builder.BuildImageOptions{
		Dockerfile:        dockerfile,
		Context:           b.contextDir,
		AllowedRegistries: b.allowedRegistries,
	}
	if ociOutput == nil {
		err = bld.Build(ctx, options)
	} else {
		err = bld.BuildOCI(ctx, options, ociOutput)
	}
	if err != nil {
		_, _ = fmt.Fprintf(logBuf, "\nbuild error: %v\n", err)
		return logBuf.String(), fmt.Errorf("build failed: %w", err)
	}
	return "", nil
}
