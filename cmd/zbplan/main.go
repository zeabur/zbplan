package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	zbplan "github.com/zeabur/zbplan/pkg/zbplan"
)

var (
	buildkitAddr      = flag.String("buildkit-addr", "", "optional: the address of the buildkit server")
	contextDir        = flag.String("context-dir", "", "the directory to use as the build context")
	dockerfilePath    = flag.String("dockerfile", "", "optional: path to an existing Dockerfile to try first")
	ociOut            = flag.String("oci-out", "", "optional: write OCI image tarball to this path")
	allowedRegistries = flag.String("allowed-registries", "", "comma-separated allowed image registries (default: docker.io,ghcr.io,quay.io,gcr.io)")
	maxBuildAttempts  = flag.Int("max-build-attempts", 0, "maximum generated Dockerfile build attempts (default 3)")
	maxAgentSteps     = flag.Int("max-agent-steps", 0, "maximum ReAct steps per generation (default 16)")
	runTimeout        = flag.Duration("run-timeout", 0, "maximum total run duration (default 15m)")
	buildTimeout      = flag.Duration("build-timeout", 0, "maximum duration of one BuildKit solve (default 10m)")
)

func main() {
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *contextDir == "" {
		slog.Error("context-dir is required")
		os.Exit(1)
	}

	chatModel, err := zbplan.NewModelFromEnv(ctx)
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
	var registries []string
	if value := strings.TrimSpace(*allowedRegistries); value != "" {
		registries = strings.Split(value, ",")
	}

	cfg := zbplan.Config{
		Model:             chatModel,
		BuildKitAddr:      *buildkitAddr,
		ContextDir:        *contextDir,
		AllowedRegistries: registries,
		UserDockerfile:    userDockerfile,
		MaxBuildAttempts:  *maxBuildAttempts,
		MaxAgentSteps:     *maxAgentSteps,
		RunTimeout:        *runTimeout,
		BuildTimeout:      *buildTimeout,
	}

	if *ociOut != "" {
		f, err := os.Create(*ociOut)
		if err != nil {
			slog.Error("failed to create oci output file", "path", *ociOut, "error", err)
			os.Exit(1)
		}
		// Run owns and closes OCIOutput.
		cfg.OCIOutput = f
	}

	result, err := zbplan.Run(ctx, cfg)
	if err != nil {
		slog.Error("planning failed", "error", err)
		os.Exit(1)
	}

	fmt.Println(result.Dockerfile)
}
