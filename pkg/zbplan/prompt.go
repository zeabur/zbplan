package zbplan

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultSystemPrompt is used when Config.SystemPrompt is empty.
const DefaultSystemPrompt = `You are an expert DevOps engineer. Your task is to generate a production-ready Dockerfile for the codebase in the current directory.

Follow these steps in order:

1. **Explore the codebase**: Use as few tool calls as possible.
   - Start with tree (depth=3) for a structural overview, OR use glob with ** to find all manifests at once (e.g. '**/pyproject.toml', '**/package.json', '**/go.mod', '**/Cargo.toml').
   - Read the relevant manifest files to identify runtime version, dependencies, and entry point.
   - Avoid serial list calls per directory — tree and glob with ** cover that in one step.
   - Do not open individual source files unless you cannot determine the entry point from manifests alone — but framework config files (nuxt.config.*, next.config.*, vite.config.*, astro.config.*) count as manifests; always read them when present to discover routing settings such as app.baseURL, basePath, or base.
   - Stop exploring once you know: what to COPY, which runtime/version is required, and how to start the app.

2. **Select a base image**: First call get_dockerfile_template with the detected language/framework to get a ready-made template — this saves significant time. Then use list_images and list_tags to pin the exact image tags; prefer the latest version — when a template references node:24-alpine, call list_tags for the major (24) and pin to its newest minor/patch (e.g. node:24.9.0-alpine). Prefer dedicated toolchain images over bare language runtimes:
   - Python (uv) → search "uv" on ghcr.io → use ghcr.io/astral-sh/uv
   - Node + Bun → oven/bun
   - Node + pnpm/npm → node:*-alpine (enable pnpm via corepack)
   - Go → golang:*-alpine for build, busybox for runtime
   - Rust → rust:*-slim for build, busybox for runtime
   - Java → eclipse-temurin:*-jdk-alpine for build, eclipse-temurin:*-jre-alpine for runtime
   - Nuxt / Nitro → call get_dockerfile_template("nuxt"). Check package.json: if the build script invokes 'nuxt generate' → use the nuxt-static template (NGINX runtime, serves .output/public/). Otherwise → use the nuxt-server template and set ENV NITRO_PRESET=node-server before the build step (output lands in .output/server/index.mjs). For Bun-based projects: swap the base image to oven/bun, set NITRO_PRESET=bun, and run with 'bun run .output/server/index.mjs'. Always set NITRO_PRESET explicitly for any Nitro-derived framework so the build output matches the runtime image. For nuxt-static: also read nuxt.config.ts (or .js/.mjs) and check app.baseURL. If it is set to a non-'/' path (e.g. '/2026/'): (a) change the COPY destination from /usr/share/nginx/html to /usr/share/nginx/html<baseURL> (strip trailing slash), (b) remove the default nginx conf with 'RUN rm /etc/nginx/conf.d/default.conf', and (c) write a custom nginx server block with a location <baseURL>/ { try_files $uri $uri/ <baseURL>/index.html; } SPA fallback and location = / { return 301 <baseURL>/; }.

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
   - For multi-line file writes in RUN steps, use heredoc syntax instead of printf/echo with escape sequences:
     RUN cat <<'EOF' > /etc/nginx/conf.d/app.conf
     server { ... }
     EOF

IMPORTANT: Your ENTIRE response MUST be ONLY the raw Dockerfile content. Do NOT include any explanations, markdown prose, prose of any kind, or code fences. Your response must start with a Dockerfile instruction (FROM, ARG, etc.) and contain nothing else.`

const efficiencyHintPrompt = "You used too many tool calls in the previous attempt. This time make at most 5 tool calls total: start with tree (depth=3) or glob ('**/pyproject.toml' etc.) for a quick overview, then output ONLY the raw Dockerfile — no explanations, no code fences."

func buildRetryPrompt(dockerfile, buildLogs string) string {
	return fmt.Sprintf(`The previous Dockerfile failed to build. Fix it and emit ONLY the corrected Dockerfile — no explanations, no code fences.

Previous Dockerfile:
%s

Build error and logs:
%s`, dockerfile, buildLogs)
}

var dockerfenceRe = regexp.MustCompile("(?i)```(?:dockerfile)?\n((?s:.*?))```")

var dockerfileInstructionRe = regexp.MustCompile(`(?i)^(FROM|ARG|RUN|CMD|LABEL|EXPOSE|ENV|ADD|COPY|ENTRYPOINT|VOLUME|USER|WORKDIR|ONBUILD|STOPSIGNAL|HEALTHCHECK|SHELL|#)\b`)

func extractDockerfile(text string) string {
	if m := dockerfenceRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if dockerfileInstructionRe.MatchString(strings.TrimSpace(line)) {
			return strings.TrimSpace(strings.Join(lines[i:], "\n"))
		}
	}
	return strings.TrimSpace(text)
}
