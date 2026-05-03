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

2. **Select a base image**: First call get_dockerfile_template with the detected language/framework to get a ready-made template — this saves significant time. Then use list_images and list_tags only to pin the exact image tags. Prefer dedicated toolchain images over bare language runtimes:
   - Python (uv) → search "uv" on ghcr.io → use ghcr.io/astral-sh/uv
   - Node + Bun → oven/bun
   - Node + pnpm/npm → node:*-alpine (enable pnpm via corepack)
   - Go → golang:*-alpine for build, busybox for runtime
   - Rust → rust:*-slim for build, busybox for runtime
   - Java → eclipse-temurin:*-jdk-alpine for build, eclipse-temurin:*-jre-alpine for runtime

3. **Write the Dockerfile** following these practices:

   **Multi-stage builds**: Use named stages (AS builder, AS runtime). Copy only the final artifact into the runtime stage.

   **Cache mounts** — always use --mount=type=cache for package manager steps so repeated builds are fast:
   - Python/uv:  RUN --mount=type=cache,target=/root/.cache/uv \
                     uv sync --frozen --no-install-project
   - Node (npm): RUN --mount=type=cache,target=/root/.npm \
                     npm ci
   - Go:         RUN --mount=type=cache,target=/go/pkg/mod \
                     --mount=type=cache,target=/root/.cache/go-build \
                     go build -o /app ./...
   - apt:        RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
                     --mount=type=cache,target=/var/lib/apt,sharing=locked \
                     apt-get update && apt-get install -y --no-install-recommends <pkgs>

   **Bind mounts** — when a file is only needed during a RUN step and must not become a layer, use --mount=type=bind instead of COPY:
   - Example:    RUN --mount=type=bind,source=pyproject.toml,target=pyproject.toml \
                     --mount=type=bind,source=uv.lock,target=uv.lock \
                     uv sync --frozen --no-install-project

   **Layer ordering**: instructions that change rarely (OS deps, dependency install) before instructions that change often (COPY source, compile).

   **Other rules**:
   - Pin image tags — never use bare latest.
   - Set WORKDIR explicitly; never use cd in RUN.
   - Create and switch to a non-root user before CMD.
   - Use exec-form CMD/ENTRYPOINT (JSON array, not shell string).
   - Use EXPOSE for the listening port.
   - With apt, pass --no-install-recommends; if not using a cache mount, end the RUN with rm -rf /var/lib/apt/lists/*.

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
		Model:     "claude-sonnet-4-6",
		MaxTokens: 8192,
		Thinking: &claude.Thinking{
			Enable:       true,
			BudgetTokens: 4096,
		},
	}
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		cfg.BaseURL = &base
	}
	if model := os.Getenv("ANTHROPIC_MODEL"); model != "" {
		cfg.Model = model
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
				plantools.NewGetDockerfileTemplateTool(),
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

// dockerfileInstructionRe matches a line that begins with a Dockerfile instruction.
var dockerfileInstructionRe = regexp.MustCompile(`(?i)^(FROM|ARG|RUN|CMD|LABEL|EXPOSE|ENV|ADD|COPY|ENTRYPOINT|VOLUME|USER|WORKDIR|ONBUILD|STOPSIGNAL|HEALTHCHECK|SHELL|#)\b`)

func extractDockerfile(text string) string {
	if m := dockerfenceRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	// Skip any prose preamble the model may have emitted before the first instruction.
	for i, line := range strings.Split(text, "\n") {
		if dockerfileInstructionRe.MatchString(strings.TrimSpace(line)) {
			return strings.TrimSpace(strings.Join(strings.Split(text, "\n")[i:], "\n"))
		}
	}
	return strings.TrimSpace(text)
}
