package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	zbplan "github.com/zeabur/zbplan/pkg/zbplan"
)

var (
	buildkitAddr   = flag.String("buildkit-addr", "", "the address of the buildkit server")
	contextDir     = flag.String("context-dir", "", "the directory to use as the build context")
	dockerfilePath = flag.String("dockerfile", "", "optional: path to an existing Dockerfile to try first")
	ociOut         = flag.String("oci-out", "", "optional: write OCI image tarball to this path")
	variables      = MapFlag{}
)

func init() {
	flag.Var(&variables, "variables", "the variables to pass to the build context")
}

func main() {
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *contextDir == "" {
		slog.Error("context-dir is required")
		os.Exit(1)
	}
	if *buildkitAddr == "" {
		slog.Error("buildkit-addr is required")
		os.Exit(1)
	}

	chatModel, err := zbplan.NewClaudeModelFromEnv(ctx)
	if err != nil {
		slog.Error("failed to create chat model", "error", err)
		os.Exit(1)
	}

	var userDockerfile string
	if *dockerfilePath != "" {
		data, err := os.ReadFile(*dockerfilePath)
		if err != nil {
			slog.Error("failed to read dockerfile", "path", *dockerfilePath, "error", err)
			os.Exit(1)
		}
		userDockerfile = string(data)
	}

	cfg := zbplan.Config{
		Model:          chatModel,
		BuildKitAddr:   *buildkitAddr,
		ContextDir:     *contextDir,
		Variables:      variables,
		UserDockerfile: userDockerfile,
	}

	if *ociOut != "" {
		f, err := os.Create(*ociOut)
		if err != nil {
			slog.Error("failed to create oci output file", "path", *ociOut, "error", err)
			os.Exit(1)
		}
		defer func() { _ = f.Close() }()
		cfg.OCIOutput = f
	}

	result, err := zbplan.Run(ctx, cfg)
	if err != nil {
		slog.Error("planning failed", "error", err)
		os.Exit(1)
	}

	fmt.Println(result.Dockerfile)
}
