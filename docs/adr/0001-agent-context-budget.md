# ADR 0001: Bound agent context at the tool boundary instead of replacing the harness

- Status: Proposed
- Date: 2026-09-18
- Related: PLA-2604, PR #19

## Context

A single retry request sent 3.09M prompt tokens in two messages (system prompt plus one user message). The user message was the retry prompt with the full BuildKit log embedded. The ReAct loop had not run a single tool call. One request cost USD 17 before cache discount.

Two fixes were on the table:

1. Cap every piece of tool output and build log before it enters a prompt, and compact old tool rounds in the ReAct history.
2. Replace the eino ReAct harness with an existing coding-agent runtime (Pi Coding Agent) and register our tools on it.

## Decision

Keep the eino harness. Enforce context limits inside zbplan:

- Every tool result is capped at 12 KiB in model input. The full result stays in an execution-local store and is readable in 8 KiB pages through `read_tool_output`.
- The retry build log is capped at 12 KiB, keeping the head and the failure tail.
- Older tool rounds are compacted to a one-line reference. As implemented in PR #19, only the latest round is kept in full. This ADR asks for a window of recent rounds instead (roadmap step 1).
- A hard ceiling on total prompt bytes is checked before every model call (roadmap step 2, not yet implemented).

## Reasons

- The 3M-token request is a prompt-construction bug, not a harness bug. No harness stops a caller from pasting a 3M-token string into a user message. The cap must live in our code either way.
- Pi is TypeScript. zbplan is Go with a Nix build. Adopting Pi means a rewrite or a Go-to-Node process boundary, both larger than the problem.
- zbplan is a narrow loop: inspect, emit one Dockerfile, build, fix, at most 3 attempts. Pi is a general interactive coding agent with sessions, extensions, and a bash tool. Most of it would be disabled.
- Prior art does the same thing we chose. SWE-agent caps observations and collapses rounds older than the last 5 to a one-line placeholder. Anthropic context editing (`clear_tool_uses`) keeps the newest tool results and clears older ones server-side. Repo2Run caps build feedback before it reaches the model.

## Consequences

- The model must call `read_tool_output` to see the middle of a large file or log. Prompts should mention this.
- Compacting only a window, not everything, means small results such as `package.json` stay visible when the model writes the final Dockerfile. Compacting everything was tried in PR #19 and forces re-reads, which conflicts with the "at most 5 tool calls" hint and can cost more than it saves.
- Revisit this decision only if zbplan grows into a general "fix the repo until it deploys" agent that edits many files and keeps sessions. That is a product change, not a token fix.

## Roadmap

Each step ships on its own. Order is by payoff per line of code.

1. **Merge the caps, narrow the rewriter.** Keep `boundedBuildLogs` and `boundedToolOutput`. Change the history rewriter to compact only results that were already truncated, or keep the last 5 tool rounds in full.
2. **Guardrail.** Sum message bytes before every model call and log it. Fail the run or emit a metric above a hard ceiling (about 200 KiB). One function, one test.
3. **Structured build failure.** Parse BuildKit output in Go: failing step, its command, last N lines of stderr for that step. Feed that to the retry prompt instead of head and tail of the raw stream.
4. **Carry established facts across attempts.** Keep a small struct of detected facts (package manager, runtime version, build command) and render it at the top of the retry prompt so the model does not re-explore the repo every attempt.
5. **Eval.** A fixed set of 30 to 50 repos with known-good Dockerfiles, run in CI. Track success rate, tool calls, and prompt tokens per run. Every step above must show up here.

Later, only if the eval numbers justify it: rollback between attempts, a second model pass to summarize old rounds, or Anthropic context editing on the Claude path.

## References

- SWE-agent: Agent-Computer Interfaces Enable Automated Software Engineering. https://arxiv.org/abs/2405.15793
- Repo2Run: Automated Building Executable Environment for Code Repository at Scale. https://arxiv.org/abs/2502.13681
- Claude context editing. https://platform.claude.com/docs/en/build-with-claude/context-editing
- Pi Coding Agent, programmatic usage. https://github.com/earendil-works/pi/tree/main/packages/coding-agent#programmatic-usage
