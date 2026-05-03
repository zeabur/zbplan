package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"

	claude "github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/zeabur/zbplan/internal/plantools"
)

var buildkitAddr = flag.String("buildkit-addr", "", "the address of the buildkit server")
var contextDir = flag.String("context-dir", "", "the directory to use as the build context")
var variables = MapFlag{}

func init() {
	flag.Var(&variables, "variables", "the variables to pass to the build context")
}

const systemPrompt = `You are an expert DevOps engineer. Your task is to generate a production-ready Dockerfile for the codebase in the current directory.

Follow these steps in order:

1. **Explore the codebase**: Use as few tool calls as possible.
   - Start with tree (depth=3) for a structural overview, OR use glob with ** to find all manifests at once (e.g. '**/pyproject.toml', '**/package.json', '**/go.mod', '**/Cargo.toml').
   - Read the relevant manifest files to identify runtime version, dependencies, and entry point.
   - Avoid serial list calls per directory — tree and glob with ** cover that in one step.
   - Do not open individual source files unless you cannot determine the entry point from manifests alone.
   - Stop exploring once you know: what to COPY, which runtime/version is required, and how to start the app.

2. **Select a base image**: Use the list_images tool to find candidate base images, then use list_tags to find a suitable, up-to-date tag that matches the detected runtime and version requirements.

3. **Output the Dockerfile**: Write a Dockerfile that correctly builds and runs the application. Follow best practices: use multi-stage builds where appropriate, minimize final image size, avoid running as root, and copy only necessary files.

IMPORTANT: Your ENTIRE response MUST be ONLY the raw Dockerfile content. Do NOT include any explanations, markdown prose, prose of any kind, or code fences. Your response must start with a Dockerfile instruction (FROM, ARG, etc.) and contain nothing else.`

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

	builderClient, err := plantools.NewBuilderClient(ctx, *buildkitAddr, *contextDir, variables)
	if err != nil {
		slog.Error("failed to create builder client", "error", err)
		os.Exit(1)
	}
	defer builderClient.Close()

	cfg := &claude.Config{
		APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		Model:     "claude-opus-4-7",
		MaxTokens: 8192,
		AdditionalRequestFields: map[string]any{
			"thinking": map[string]any{
				"type": "adaptive",
			},
		},
	}
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		cfg.BaseURL = &base
	}
	chatModel, err := claude.NewChatModel(ctx, cfg)
	if err != nil {
		slog.Error("failed to create chat model", "error", err)
		os.Exit(1)
	}

	loggingMiddleware := compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				slog.InfoContext(ctx, "tool call", "name", input.Name, "args", input.Arguments)
				out, err := next(ctx, input)
				if err != nil {
					slog.ErrorContext(ctx, "tool error", "name", input.Name, "error", err)
					return nil, err
				}
				snippet := out.Result
				if len(snippet) > 100 {
					snippet = snippet[:100] + "..."
				}
				slog.InfoContext(ctx, "tool result", "name", input.Name, "result", snippet)
				return out, nil
			}
		},
	}

	reactAgent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{
				plantools.NewListImagesTool(),
				plantools.NewListTagsTool(),
				plantools.NewTreeTool(*contextDir),
				plantools.NewGlobTool(*contextDir),
				plantools.NewGrepTool(*contextDir),
				plantools.NewReadTool(*contextDir),
				plantools.NewListTool(*contextDir),
			},
			ToolCallMiddlewares: []compose.ToolMiddleware{loggingMiddleware},
		},
		MaxStep: 100,
	})
	if err != nil {
		slog.Error("failed to create agent", "error", err)
		os.Exit(1)
	}

	const maxBuildAttempts = 3
	prompt := "Generate the Dockerfile for the codebase in the current directory."
	var lastDockerfile string

	for attempt := 1; attempt <= maxBuildAttempts; attempt++ {
		slog.Info("generating dockerfile", "attempt", attempt)

		msg, err := reactAgent.Generate(ctx, []*schema.Message{
			{Role: schema.System, Content: systemPrompt},
			{Role: schema.User, Content: prompt},
		})
		if err != nil {
			if strings.Contains(err.Error(), "exceeds max steps") {
				slog.Warn("agent exceeded max steps, retrying with efficiency hint", "attempt", attempt)
				prompt = "You used too many tool calls in the previous attempt. This time make at most 5 tool calls total: start with tree (depth=3) or glob ('**/pyproject.toml' etc.) for a quick overview, then output ONLY the raw Dockerfile — no explanations, no code fences."
				continue
			}
			slog.Error("failed to generate dockerfile", "error", err)
			os.Exit(1)
		}

		dockerfile := extractDockerfile(msg.Content)
		lastDockerfile = dockerfile

		slog.Info("trying to build dockerfile", "attempt", attempt)
		buildLogs, buildErr := builderClient.RunBuild(ctx, dockerfile)
		if buildErr == nil {
			fmt.Println(dockerfile)
			return
		}

		slog.Warn("build failed", "attempt", attempt, "error", buildErr)
		prompt = fmt.Sprintf(`The previous Dockerfile failed to build. Fix it and emit ONLY the corrected Dockerfile — no explanations, no code fences.

Previous Dockerfile:
%s

Build error and logs:
%s`, dockerfile, buildLogs)
	}

	slog.Error("dockerfile failed to build after all retries", "attempts", maxBuildAttempts, "last_dockerfile", lastDockerfile)
	os.Exit(1)
}

var dockerfenceRe = regexp.MustCompile("(?i)```(?:dockerfile)?\n((?s:.*?))```")

func extractDockerfile(text string) string {
	if m := dockerfenceRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(text)
}
