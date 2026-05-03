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

	"github.com/zeabur/zbplan/internal/plantools"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider/compat"
)

type GeneratedDockerfile struct {
	Dockerfile string `json:"dockerfile"`
}

var buildkitAddr = flag.String("buildkit-addr", "", "the address of the buildkit server")
var contextDir = flag.String("context-dir", "", "the directory to use as the build context")
var variables = MapFlag{}

func init() {
	flag.Var(&variables, "variables", "the variables to pass to the build context")
}

func main() {
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	model := compat.Chat("claude-opus-4-7",
		compat.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
		compat.WithBaseURL(os.Getenv("OPENAI_BASE_URL")),
	)

	if contextDir == nil || *contextDir == "" {
		slog.Error("context-dir is required")
		os.Exit(1)
	}

	if buildkitAddr == nil || *buildkitAddr == "" {
		slog.Error("buildkit-addr is required")
		os.Exit(1)
	}

	contextDirFS := os.DirFS(*contextDir)

	planTools := plantools.BuilderBaseContext{
		BuildkitAddr: *buildkitAddr,
		ContextDir:   *contextDir,
		Variables:    variables,
	}

	builderTool, err := plantools.NewDockerfileCanBuildTool(ctx, planTools)
	if err != nil {
		slog.Error("failed to create build_and_check_dockerfile tool", "error", err)
		os.Exit(1)
	}

	result, err := goai.GenerateText(ctx, model,
		goai.WithSystem(`You are an expert DevOps engineer. Your task is to generate a production-ready Dockerfile for the codebase in the current directory.

Follow these steps in order:

1. **Explore the codebase**: Use the list, glob, grep, and read tools to understand the project structure, language/runtime, dependencies, entry points, and any existing build configuration (e.g. package.json, go.mod, requirements.txt, Makefile, etc.). Use as few tool calls as possible — stop once you know the code structure (what to copy), required versions, and startup parameters.

2. **Select a base image**: Use the list_images tool to find candidate base images, then use list_tags to find a suitable, up-to-date tag that matches the detected runtime and version requirements.

3. **Draft a Dockerfile**: Write a Dockerfile that correctly builds and runs the application. Follow best practices: use multi-stage builds where appropriate, minimize final image size, avoid running as root, and copy only necessary files. Prefer fast Dockerfile iteration over exhaustive code exploration.

4. **Verify the build**: Call build_and_check_dockerfile with the drafted Dockerfile. If the build fails, read the error output, fix the Dockerfile, and retry until the build succeeds.

IMPORTANT: Your final response MUST contain only the raw Dockerfile content — no explanations, no markdown prose, no comments outside the Dockerfile itself. Output the Dockerfile and nothing else.`),
		goai.WithPrompt("Dockerfile:"),
		goai.WithTools(
			builderTool,
			plantools.NewListImagesTool(),
			plantools.NewListTagsTool(),
			plantools.NewGlobTool(contextDirFS),
			plantools.NewGrepTool(contextDirFS),
			plantools.NewReadTool(contextDirFS),
			plantools.NewListTool(contextDirFS),
		),
		goai.WithProviderOptions(map[string]any{
			"thinking": map[string]any{
				"type": "adaptive",
			},
		}),
		goai.WithToolChoice("auto"),
		goai.WithMaxSteps(25),
		goai.WithOnStepFinish(func(step goai.StepResult) {
			fmt.Printf("--- Step %d (finish: %s, tools: %d) ---\n",
				step.Number, step.FinishReason, len(step.ToolCalls))
		}),
		goai.WithOnToolCallStart(func(tcsi goai.ToolCallStartInfo) {
			fmt.Printf("  Tool (start): %s: %s\n", tcsi.ToolName, tcsi.Input)
		}),
		goai.WithOnToolCall(func(info goai.ToolCallInfo) {
			fmt.Printf("  Tool: %s: %s -> %s...\n", info.ToolName, info.Input, info.Output[:min(50, len(info.Output))])
		}),
	)
	if err != nil {
		slog.Error("failed to generate dockerfile", "error", err)
		os.Exit(1)
	}

	fmt.Println(extractDockerfile(result.Text))
	fmt.Printf("# Tokens: %d in, %d out, %d for reasoning.\n",
		result.TotalUsage.InputTokens, result.TotalUsage.OutputTokens, result.TotalUsage.ReasoningTokens)
}

var dockerfenceRe = regexp.MustCompile("(?i)```(?:dockerfile)?\n((?s:.*?))```")

func extractDockerfile(text string) string {
	if m := dockerfenceRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(text)
}
