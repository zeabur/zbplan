//go:build integration

package builder_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/zeabur/zbplan/pkg/buildenvtest"
	"github.com/zeabur/zbplan/pkg/builder"
)

type outputBuffer struct{ bytes.Buffer }

func (*outputBuffer) Close() error { return nil }

func TestBuildEnvironmentIntegration(t *testing.T) {
	buildenvtest.Run(t, func(ctx context.Context, dockerfile, dir string, vars map[string]string, log io.Writer) ([]byte, error) {
		c, err := client.New(ctx, os.Getenv("BUILDKIT_HOST"))
		if err != nil {
			return nil, err
		}
		defer func() { _ = c.Close() }()
		b := builder.NewBuildkitBuilder(c, slog.New(slog.NewTextHandler(log, nil)))
		var output outputBuffer
		err = b.BuildOCI(ctx, builder.BuildImageOptions{Dockerfile: dockerfile, Context: dir, Variables: vars}, &output)
		return output.Bytes(), err
	})
}
