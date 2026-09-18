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
	buildkitAddr               = flag.String("buildkit-addr", "", "optional: the address of the buildkit server")
	contextDir                 = flag.String("context-dir", "", "the directory to use as the build context")
	dockerfilePath             = flag.String("dockerfile", "", "optional: path to an existing Dockerfile to try first")
	ociOut                     = flag.String("oci-out", "", "optional: write OCI image tarball to this path")
	allowNetwork               = flag.Bool("allow-build-network", false, "allow Dockerfile RUN instructions to use BuildKit's default network")
	maxBuildAttempts           = flag.Int("max-build-attempts", 0, "maximum generated Dockerfile build attempts")
	maxAgentSteps              = flag.Int("max-agent-steps", 0, "maximum ReAct graph steps per generation")
	maxModelRequests           = flag.Int("max-model-requests", 0, "maximum model requests per run")
	maxToolCalls               = flag.Int("max-tool-calls", 0, "maximum tool calls per run")
	maxParallelToolCalls       = flag.Int("max-parallel-tool-calls", 0, "maximum simultaneous tool calls")
	maxRetainedToolOutputBytes = flag.Int("max-retained-tool-output-bytes", 0, "maximum bytes retained across full tool outputs")
	maxBuildLogBytes           = flag.Int("max-build-log-bytes", 0, "maximum BuildKit log bytes retained per attempt")
	runTimeout                 = flag.Duration("run-timeout", 0, "maximum total run duration")
	toolTimeout                = flag.Duration("tool-timeout", 0, "maximum duration of one tool call")
	buildTimeout               = flag.Duration("build-timeout", 0, "maximum duration of one BuildKit solve")
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

	cfg := zbplan.Config{
		Model:             chatModel,
		BuildKitAddr:      *buildkitAddr,
		ContextDir:        *contextDir,
		AllowBuildNetwork: *allowNetwork,
		UserDockerfile:    userDockerfile,
		Limits: zbplan.Limits{
			MaxBuildAttempts:           *maxBuildAttempts,
			MaxAgentSteps:              *maxAgentSteps,
			MaxModelRequests:           *maxModelRequests,
			MaxToolCalls:               *maxToolCalls,
			MaxParallelToolCalls:       *maxParallelToolCalls,
			MaxRetainedToolOutputBytes: *maxRetainedToolOutputBytes,
			MaxBuildLogBytes:           *maxBuildLogBytes,
			RunTimeout:                 *runTimeout,
			ToolTimeout:                *toolTimeout,
			BuildTimeout:               *buildTimeout,
		},
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
	slog.Info("planning usage",
		"model_requests", result.Stats.ModelRequests,
		"tool_calls", result.Stats.ToolCalls,
		"build_solves", result.Stats.BuildSolves,
		"retained_tool_output_bytes", result.Stats.RetainedToolOutputBytes,
		"duration", result.Stats.Duration,
	)
}
