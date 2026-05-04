package zbplan

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	claude "github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/zeabur/zbplan/internal/plantools"
)

// Config controls a single Run invocation.
type Config struct {
	// Required fields.

	// Model is the eino tool-calling model that drives the planning agent.
	// Use NewClaudeModel or NewClaudeModelFromEnv for a Claude-backed model.
	Model model.ToolCallingChatModel
	// BuildKitAddr is the BuildKit daemon endpoint
	// (e.g. "tcp://host:1234" or "docker-container://buildkitd").
	BuildKitAddr string
	// ContextDir is the source-code directory to plan for.
	ContextDir string

	// Optional fields.

	// Variables are injected as ZEABUR_ENV_* build args into every FROM stage.
	Variables map[string]string
	// UserDockerfile is an existing Dockerfile to try before invoking the agent.
	// If it builds successfully the agent is skipped entirely.
	// If it fails, the agent receives it alongside the build error as its
	// starting context. The attempt does not count toward MaxBuildAttempts.
	UserDockerfile string
	// OCIOutput receives the OCI image tarball on a successful build.
	// When nil no OCI tarball is produced (cheaper — only a verify build runs).
	//
	// io.WriteCloser is required (rather than io.Writer) because BuildKit's
	// session layer calls Close() as part of stream finalization: it flushes
	// any trailing bytes and propagates close errors back into the solve result.
	// This means Close() carries real semantics — wrapping formats such as
	// gzip or zstd rely on it to write their closing blocks, and an
	// io.PipeWriter relies on it to signal EOF to the reader.
	//
	// BuildKit calls Close() before Run returns, so callers do not need to
	// close OCIOutput themselves. Closing it again after Run is harmless for
	// most implementations (e.g. *os.File silently returns an error that can
	// be ignored), but is unnecessary.
	OCIOutput io.WriteCloser
	// ExtraTools are appended to the default eight plantools tools.
	ExtraTools []tool.BaseTool
	// SystemPrompt overrides DefaultSystemPrompt.
	SystemPrompt string
	// MaxBuildAttempts is the number of agent generate→build cycles before
	// Run returns an error. Defaults to 3.
	MaxBuildAttempts int
	// MaxAgentSteps is the maximum number of ReAct steps per Generate call.
	// Defaults to 100.
	MaxAgentSteps int
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
}

// Run generates a working Dockerfile for cfg.ContextDir and returns it.
// If cfg.OCIOutput is non-nil, the built OCI tarball is also streamed there.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.Model == nil {
		return nil, fmt.Errorf("zbplan: Model is required")
	}
	if cfg.BuildKitAddr == "" {
		return nil, fmt.Errorf("zbplan: BuildKitAddr is required")
	}
	if cfg.ContextDir == "" {
		return nil, fmt.Errorf("zbplan: ContextDir is required")
	}
	if cfg.MaxBuildAttempts == 0 {
		cfg.MaxBuildAttempts = 3
	}
	if cfg.MaxAgentSteps == 0 {
		cfg.MaxAgentSteps = 100
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultSystemPrompt
	}

	builderClient, err := plantools.NewBuilderClient(ctx, cfg.BuildKitAddr, cfg.ContextDir, cfg.Variables)
	if err != nil {
		return nil, fmt.Errorf("zbplan: create builder client: %w", err)
	}
	defer func() { _ = builderClient.Close() }()

	// Try the caller-supplied Dockerfile first; this doesn't consume an agent attempt.
	prompt := "Generate the Dockerfile for the codebase in the current directory."
	if cfg.UserDockerfile != "" {
		cfg.Logger.InfoContext(ctx, "trying user-provided dockerfile")
		buildLogs, buildErr := builderClient.RunBuild(ctx, cfg.UserDockerfile)
		if buildErr == nil {
			if cfg.OCIOutput != nil {
				if err := builderClient.RunBuildOCI(ctx, cfg.UserDockerfile, cfg.OCIOutput); err != nil {
					return nil, fmt.Errorf("zbplan: produce oci tarball: %w", err)
				}
			}
			return &Result{Dockerfile: cfg.UserDockerfile, FromUser: true}, nil
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
	}
	tools = append(tools, cfg.ExtraTools...)

	reactAgent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: cfg.Model,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			ToolCallMiddlewares: []compose.ToolMiddleware{newLoggingMiddleware(cfg.Logger)},
		},
		MaxStep: cfg.MaxAgentSteps,
	})
	if err != nil {
		return nil, fmt.Errorf("zbplan: create agent: %w", err)
	}

	var lastDockerfile string
	for attempt := 1; attempt <= cfg.MaxBuildAttempts; attempt++ {
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
				prompt = efficiencyHintPrompt
				continue
			}
			return nil, fmt.Errorf("zbplan: generate dockerfile: %w", err)
		}

		dockerfile := extractDockerfile(msg.Content)
		lastDockerfile = dockerfile

		cfg.Logger.InfoContext(ctx, "trying to build dockerfile", "attempt", attempt)
		buildLogs, buildErr := builderClient.RunBuild(ctx, dockerfile)
		if buildErr == nil {
			if cfg.OCIOutput != nil {
				if err := builderClient.RunBuildOCI(ctx, dockerfile, cfg.OCIOutput); err != nil {
					return nil, fmt.Errorf("zbplan: produce oci tarball: %w", err)
				}
			}
			return &Result{Dockerfile: dockerfile, Attempts: attempt}, nil
		}

		cfg.Logger.WarnContext(ctx, "build failed", "attempt", attempt, "error", buildErr)
		prompt = buildRetryPrompt(dockerfile, buildLogs)
	}

	return nil, fmt.Errorf("zbplan: dockerfile failed to build after %d attempts; last dockerfile:\n%s",
		cfg.MaxBuildAttempts, lastDockerfile)
}

func newLoggingMiddleware(logger *slog.Logger) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				logger.DebugContext(ctx, "tool call", "name", input.Name, "args", input.Arguments)
				out, err := next(ctx, input)
				if err != nil {
					logger.ErrorContext(ctx, "tool error", "name", input.Name, "error", err)
					return nil, err
				}
				snippet := out.Result
				if len(snippet) > 100 {
					snippet = snippet[:100] + "..."
				}
				logger.DebugContext(ctx, "tool result", "name", input.Name, "result", snippet)
				return out, nil
			}
		},
	}
}
