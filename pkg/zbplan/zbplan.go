package zbplan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	claude "github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/zeabur/zbplan/internal/plantools"
	buildpkg "github.com/zeabur/zbplan/pkg/builder"
)

// Config controls a single Run invocation.
type Config struct {
	// Model is the eino tool-calling model that drives the planning agent.
	// Use NewClaudeModel or NewClaudeModelFromEnv for a Claude-backed model.
	Model model.ToolCallingChatModel

	// BuildKitAddr is the optional BuildKit daemon endpoint
	// (e.g. "tcp://host:1234" or "docker-container://buildkitd").
	// When empty, BuildKit's client falls back to the system default address.
	BuildKitAddr string

	// ContextDir is the source-code directory to plan for.
	ContextDir string

	// AllowBuildNetwork opts generated and user-provided Dockerfiles into the
	// BuildKit default network. It is disabled by default because repository
	// content and model output are untrusted.
	AllowBuildNetwork bool
	// UserDockerfile is an existing Dockerfile to try before invoking the agent.
	// If it builds successfully the agent is skipped entirely.
	// If it fails, the agent receives it alongside the build error as its
	// starting context. The attempt does not count toward Limits.MaxBuildAttempts.
	UserDockerfile string
	// OCIOutput receives the OCI image tarball from the same BuildKit solve
	// that verifies the successful Dockerfile. Failed attempts write only to
	// temporary files, so retries do not corrupt this output.
	//
	// io.WriteCloser is required because Close carries real semantics:
	// wrapping formats such as gzip or zstd write closing blocks, and an
	// io.PipeWriter signals EOF. Run closes OCIOutput before it returns.
	OCIOutput io.WriteCloser
	// ExtraTools are appended to the default plantools tools. They share the
	// same call, concurrency, timeout, logging, and output-retention budgets.
	ExtraTools []tool.BaseTool
	// SystemPrompt overrides DefaultSystemPrompt.
	SystemPrompt string
	// Limits bounds work for this run. Zero-valued fields use DefaultLimits.
	Limits Limits
	// Logger defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// Result is returned by a successful Run call.
type Result struct {
	// Dockerfile is the working Dockerfile text.
	Dockerfile string
	// Attempts is the number of agent generate→build cycles used.
	// Zero means UserDockerfile built successfully without invoking the agent.
	Attempts int
	// FromUser is true when Dockerfile is the unchanged UserDockerfile.
	FromUser bool
	// Stats reports host-observed work for this run.
	Stats RunStats
}

// RunError reports host-observed usage when a run fails after initialization.
type RunError struct {
	Err   error
	Stats RunStats
}

func (e *RunError) Error() string { return e.Err.Error() }
func (e *RunError) Unwrap() error { return e.Err }

// Run generates a working Dockerfile for cfg.ContextDir and returns it.
// If cfg.OCIOutput is non-nil, the built OCI tarball is also streamed there.
func Run(ctx context.Context, cfg Config) (result *Result, err error) {
	started := time.Now()
	if cfg.Model == nil {
		return nil, fmt.Errorf("zbplan: Model is required")
	}
	if cfg.ContextDir == "" {
		return nil, fmt.Errorf("zbplan: ContextDir is required")
	}
	limits, err := cfg.Limits.normalized()
	if err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultSystemPrompt
	}

	ctx, cancel := context.WithTimeout(ctx, limits.RunTimeout)
	defer cancel()

	networkMode := buildpkg.NetworkNone
	if cfg.AllowBuildNetwork {
		networkMode = buildpkg.NetworkDefault
	}
	builderClient, err := plantools.NewBuilderClient(ctx, plantools.BuilderClientConfig{
		Addr:        cfg.BuildKitAddr,
		ContextDir:  cfg.ContextDir,
		NetworkMode: networkMode,
		Timeout:     limits.BuildTimeout,
		MaxLogBytes: limits.MaxBuildLogBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("zbplan: create builder client: %w", err)
	}
	defer func() { _ = builderClient.Close() }()

	outputStore := newToolOutputStore(limits.MaxRetainedToolOutputBytes)
	budgetedModel := newBudgetedModel(cfg.Model, limits.MaxModelRequests)
	toolsBudget := newToolBudget(limits.MaxToolCalls, limits.MaxParallelToolCalls, limits.ToolTimeout)
	buildSolves := 0
	stats := func() RunStats {
		return RunStats{
			ModelRequests:           budgetedModel.calls(),
			ToolCalls:               toolsBudget.count(),
			BuildSolves:             buildSolves,
			RetainedToolOutputBytes: outputStore.size(),
			Duration:                time.Since(started),
		}
	}
	defer func() {
		runStats := stats()
		cfg.Logger.InfoContext(ctx, "planning run finished",
			"model_requests", runStats.ModelRequests,
			"tool_calls", runStats.ToolCalls,
			"build_solves", runStats.BuildSolves,
			"retained_tool_output_bytes", runStats.RetainedToolOutputBytes,
			"duration", runStats.Duration,
		)
		if err != nil {
			err = &RunError{Err: err, Stats: runStats}
		}
	}()
	if cfg.OCIOutput != nil {
		defer func() {
			if closeErr := cfg.OCIOutput.Close(); closeErr != nil && err == nil {
				result = nil
				err = fmt.Errorf("zbplan: close OCI output: %w", closeErr)
			}
		}()
	}

	build := func(dockerfile string) (string, error) {
		buildSolves++
		return runBuildOnce(ctx, builderClient, dockerfile, cfg.OCIOutput)
	}

	// Try the caller-supplied Dockerfile first; this doesn't consume an agent attempt.
	prompt := "Generate the Dockerfile for the codebase in the current directory."
	if cfg.UserDockerfile != "" {
		cfg.Logger.InfoContext(ctx, "trying user-provided dockerfile")
		buildLogs, buildErr := build(cfg.UserDockerfile)
		if buildErr == nil {
			return &Result{
				Dockerfile: cfg.UserDockerfile,
				FromUser:   true,
				Stats:      stats(),
			}, nil
		}
		if errors.Is(buildErr, errOCIOutput) {
			return nil, fmt.Errorf("zbplan: write OCI output: %w", buildErr)
		}
		cfg.Logger.WarnContext(ctx, "user dockerfile failed to build, handing to agent", "error", buildErr)
		prompt = buildRetryPrompt(cfg.UserDockerfile, buildLogs)
	}

	tools := []tool.BaseTool{
		plantools.NewGetDockerfileTemplateTool(),
		plantools.NewListImagesTool(),
		plantools.NewListTagsTool(),
		plantools.NewTreeTool(cfg.ContextDir),
		plantools.NewGlobTool(cfg.ContextDir),
		plantools.NewGrepTool(cfg.ContextDir),
		plantools.NewReadTool(cfg.ContextDir),
		plantools.NewListTool(cfg.ContextDir),
		newReadToolOutputTool(outputStore),
	}
	tools = append(tools, cfg.ExtraTools...)

	reactAgent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: budgetedModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: tools,
			ToolCallMiddlewares: []compose.ToolMiddleware{
				toolsBudget.middleware(),
				newToolOutputMiddleware(outputStore, cfg.Logger),
			},
		},
		MessageRewriter: newToolHistoryRewriter(outputStore),
		MaxStep:         limits.MaxAgentSteps,
	})
	if err != nil {
		return nil, fmt.Errorf("zbplan: create agent: %w", err)
	}

	for attempt := 1; attempt <= limits.MaxBuildAttempts; attempt++ {
		cfg.Logger.InfoContext(ctx, "generating dockerfile", "attempt", attempt)

		systemMsg := claude.SetMessageCacheControl(
			&schema.Message{Role: schema.System, Content: cfg.SystemPrompt},
			&claude.CacheControl{TTL: claude.CacheTTL1h},
		)
		msg, err := reactAgent.Generate(ctx, []*schema.Message{
			systemMsg,
			{Role: schema.User, Content: prompt},
		})
		if err != nil {
			if strings.Contains(err.Error(), "exceeds max steps") {
				cfg.Logger.WarnContext(ctx, "agent exceeded max steps, retrying with efficiency hint", "attempt", attempt)
				prompt = withToolCatalog(efficiencyHintPrompt, outputStore)
				continue
			}
			return nil, fmt.Errorf("zbplan: generate dockerfile: %w", err)
		}

		dockerfile := extractDockerfile(msg.Content)
		cfg.Logger.InfoContext(ctx, "trying to build dockerfile", "attempt", attempt)
		buildLogs, buildErr := build(dockerfile)
		if buildErr == nil {
			return &Result{
				Dockerfile: dockerfile,
				Attempts:   attempt,
				Stats:      stats(),
			}, nil
		}
		if errors.Is(buildErr, errOCIOutput) {
			return nil, fmt.Errorf("zbplan: write OCI output: %w", buildErr)
		}

		cfg.Logger.WarnContext(ctx, "build failed", "attempt", attempt, "error", buildErr)
		prompt = withToolCatalog(buildRetryPrompt(dockerfile, buildLogs), outputStore)
	}

	return nil, fmt.Errorf("zbplan: dockerfile failed to build after %d attempts", limits.MaxBuildAttempts)
}

func withToolCatalog(prompt string, store *toolOutputStore) string {
	catalog := store.catalog(4 * 1024)
	if catalog == "" {
		return prompt
	}
	return prompt + "\n\nPrior tool outputs remain available by reference:\n" + catalog
}

var errOCIOutput = errors.New("OCI output failure")

func runBuildOnce(ctx context.Context, builderClient *plantools.BuilderClient, dockerfile string, output io.WriteCloser) (string, error) {
	if output == nil {
		return builderClient.RunBuild(ctx, dockerfile, nil)
	}

	temp, err := os.CreateTemp("", "zbplan-oci-*.tar")
	if err != nil {
		return "", fmt.Errorf("%w: create temporary artifact: %v", errOCIOutput, err)
	}
	name := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(name)
	}()

	logs, err := builderClient.RunBuild(ctx, dockerfile, temp)
	if err != nil {
		return logs, err
	}

	artifact, err := os.Open(name)
	if err != nil {
		return logs, fmt.Errorf("%w: open temporary artifact: %v", errOCIOutput, err)
	}
	defer func() { _ = artifact.Close() }()
	if _, err := io.Copy(output, artifact); err != nil {
		return logs, fmt.Errorf("%w: copy artifact: %v", errOCIOutput, err)
	}
	return logs, nil
}
