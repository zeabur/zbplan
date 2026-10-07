package builder

import (
	"fmt"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/zeabur/zbplan/pkg/registryutil"
)

const (
	maxDockerfileBytes        = 256 * 1024
	maxDockerfileInstructions = 256

	// bundledDockerfileFrontend is passed as the frontend "cmdline" attribute.
	// Its presence stops dockerfile.v0 from forwarding to an external frontend
	// image named by a # syntax directive or BUILDKIT_SYNTAX, so the daemon's
	// bundled parser is the only code that interprets the Dockerfile.
	bundledDockerfileFrontend = "dockerfile.v0"
)

// buildLocalSources are the local inputs SolveOpt.LocalMounts provides.
var buildLocalSources = []string{"context", "dockerfile"}

// validateDockerfile bounds parser work before a Dockerfile reaches BuildKit.
// It is not the security boundary: what the build may fetch and which
// privileges it may use are enforced by BuildKit itself through sourcePolicy,
// the pinned bundled frontend, and an empty entitlement set.
func validateDockerfile(dockerfile string) error {
	if len(dockerfile) > maxDockerfileBytes {
		return fmt.Errorf("dockerfile is %d bytes; maximum is %d", len(dockerfile), maxDockerfileBytes)
	}
	parsed, err := parser.Parse(strings.NewReader(dockerfile))
	if err != nil {
		return fmt.Errorf("parse dockerfile: %w", err)
	}
	if len(parsed.AST.Children) == 0 {
		return fmt.Errorf("dockerfile has no instructions")
	}
	count := 0
	for _, instruction := range parsed.AST.Children {
		if err := countDockerfileInstructions(instruction, &count); err != nil {
			return err
		}
	}
	return nil
}

func countDockerfileInstructions(instruction *parser.Node, count *int) error {
	*count++
	if *count > maxDockerfileInstructions {
		return fmt.Errorf("dockerfile has more than %d instructions", maxDockerfileInstructions)
	}
	for _, child := range instruction.Children {
		if err := countDockerfileInstructions(child, count); err != nil {
			return err
		}
	}
	// ONBUILD stores its trigger instruction as a child of its argument node.
	for argument := instruction.Next; argument != nil; argument = argument.Next {
		for _, child := range argument.Children {
			if err := countDockerfileInstructions(child, count); err != nil {
				return err
			}
		}
	}
	return nil
}

// sourcePolicy builds the BuildKit source policy for one solve. BuildKit
// evaluates it against every source the solve resolves after the frontend has
// applied Dockerfile semantics: FROM, COPY --from, RUN --mount from=, ONBUILD
// triggers inherited from base images, ADD URLs and Git sources alike. Rules
// are evaluated in order and the last match wins, so the leading wildcard
// deny makes everything not explicitly allowed fail closed.
func sourcePolicy(allowedRegistries []string) (*spb.Policy, error) {
	registries, err := registryutil.NormalizeAllowedRegistries(allowedRegistries)
	if err != nil {
		return nil, fmt.Errorf("allowed registries: %w", err)
	}

	rules := make([]*spb.Rule, 0, 1+len(buildLocalSources)+len(registries))
	rules = append(rules, &spb.Rule{
		Action:   spb.PolicyAction_DENY,
		Selector: &spb.Selector{Identifier: "*"},
	})
	for _, name := range buildLocalSources {
		rules = append(rules, &spb.Rule{
			Action: spb.PolicyAction_ALLOW,
			Selector: &spb.Selector{
				Identifier: "local://" + name,
				MatchType:  spb.MatchType_EXACT,
			},
		})
	}
	for _, registry := range registries {
		// Registry names are validated host[:port] values and cannot contain
		// wildcard metacharacters. BuildKit normalizes image identifiers
		// (alpine → docker.io/library/alpine:latest) before evaluation.
		rules = append(rules, &spb.Rule{
			Action: spb.PolicyAction_ALLOW,
			Selector: &spb.Selector{
				Identifier: "docker-image://" + registry + "/*",
				MatchType:  spb.MatchType_WILDCARD,
			},
		})
	}
	return &spb.Policy{Version: 1, Rules: rules}, nil
}
