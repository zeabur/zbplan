package builder

import (
	"fmt"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/zeabur/zbplan/pkg/registryutil"
)

const (
	maxDockerfileBytes        = 256 * 1024
	maxDockerfileInstructions = 256
)

func validateBuildPolicy(dockerfile string, allowedRegistries []string) error {
	if len(dockerfile) > maxDockerfileBytes {
		return fmt.Errorf("dockerfile is %d bytes; maximum is %d", len(dockerfile), maxDockerfileBytes)
	}

	for line := range strings.SplitSeq(dockerfile, "\n") {
		directive := strings.TrimSpace(line)
		if !strings.HasPrefix(directive, "#") {
			continue
		}
		name, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(directive, "#")), "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), "syntax") && !allowedDockerfileFrontend(value) {
			return fmt.Errorf("custom Dockerfile syntax frontends are not allowed")
		}
	}

	parsed, err := parser.Parse(strings.NewReader(dockerfile))
	if err != nil {
		return fmt.Errorf("parse dockerfile: %w", err)
	}
	if len(parsed.AST.Children) == 0 {
		return fmt.Errorf("dockerfile has no instructions")
	}
	instructionCount := 0
	allowed, err := registryAllowlist(allowedRegistries)
	if err != nil {
		return fmt.Errorf("allowed registries: %w", err)
	}

	for _, instruction := range parsed.AST.Children {
		if err := validateDockerfileInstruction(instruction, &instructionCount, allowed); err != nil {
			return err
		}
	}

	return nil
}

func validateDockerfileInstruction(instruction *parser.Node, count *int, allowedRegistries map[string]struct{}) error {
	*count++
	if *count > maxDockerfileInstructions {
		return fmt.Errorf("dockerfile has more than %d instructions", maxDockerfileInstructions)
	}

	switch strings.ToLower(instruction.Value) {
	case "add":
		return fmt.Errorf("ADD is not allowed at line %d; use COPY for local build-context files", instruction.StartLine)
	case "from":
		if instruction.Next == nil {
			return fmt.Errorf("FROM has no image at line %d", instruction.StartLine)
		}
		if err := validateImageReference(instruction.Next.Value, allowedRegistries, false); err != nil {
			return fmt.Errorf("FROM image at line %d: %w", instruction.StartLine, err)
		}
	case "copy":
		for _, flag := range instruction.Flags {
			flag = strings.TrimPrefix(strings.ToLower(flag), "--")
			if source, ok := strings.CutPrefix(flag, "from="); ok {
				if err := validateImageReference(source, allowedRegistries, true); err != nil {
					return fmt.Errorf("COPY --from at line %d: %w", instruction.StartLine, err)
				}
			}
		}
	case "run":
		for _, flag := range instruction.Flags {
			flag = strings.TrimPrefix(strings.ToLower(flag), "--")
			if flag == "network=host" || flag == "security=insecure" {
				return fmt.Errorf("RUN --%s is not allowed at line %d", flag, instruction.StartLine)
			}
		}
	}
	for _, child := range instruction.Children {
		if err := validateDockerfileInstruction(child, count, allowedRegistries); err != nil {
			return err
		}
	}
	for argument := instruction.Next; argument != nil; argument = argument.Next {
		for _, child := range argument.Children {
			if err := validateDockerfileInstruction(child, count, allowedRegistries); err != nil {
				return err
			}
		}
	}
	return nil
}

func allowedDockerfileFrontend(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "docker/dockerfile:") ||
		strings.HasPrefix(value, "docker/dockerfile@sha256:") ||
		strings.HasPrefix(value, "docker.io/docker/dockerfile:") ||
		strings.HasPrefix(value, "docker.io/docker/dockerfile@sha256:")
}

func registryAllowlist(configured []string) (map[string]struct{}, error) {
	registries, err := registryutil.NormalizeAllowedRegistries(configured)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(registries))
	for _, registry := range registries {
		allowed[registry] = struct{}{}
	}
	return allowed, nil
}

func validateImageReference(image string, allowedRegistries map[string]struct{}, allowStageReference bool) error {
	image = strings.TrimSpace(strings.ToLower(image))
	if image == "" {
		return fmt.Errorf("image is empty")
	}
	if image == "scratch" {
		return nil
	}

	firstComponent, _, hasSlash := strings.Cut(image, "/")
	name := firstComponent
	if colon := strings.IndexByte(name, ':'); colon >= 0 {
		name = name[:colon]
	}
	if strings.ContainsAny(name, "$[]") {
		return fmt.Errorf("variable or address-based image registries are not allowed")
	}
	if allowStageReference && !hasSlash && !strings.ContainsAny(firstComponent, ".:") {
		return nil
	}

	registry := firstComponent
	if !hasSlash || (!strings.ContainsAny(firstComponent, ".:") && firstComponent != "localhost") {
		registry = "docker.io"
	}
	if _, ok := allowedRegistries[registry]; ok {
		return nil
	}
	return fmt.Errorf("registry %q is not allowed", registry)
}
