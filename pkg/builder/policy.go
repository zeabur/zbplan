package builder

import (
	"fmt"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

const (
	NetworkNone    = "none"
	NetworkDefault = "default"

	maxDockerfileBytes        = 256 * 1024
	maxDockerfileInstructions = 256
)

func validateBuildPolicy(dockerfile, networkMode string) (string, error) {
	if len(dockerfile) > maxDockerfileBytes {
		return "", fmt.Errorf("dockerfile is %d bytes; maximum is %d", len(dockerfile), maxDockerfileBytes)
	}

	if networkMode == "" {
		networkMode = NetworkNone
	}
	switch networkMode {
	case NetworkNone, NetworkDefault:
	default:
		return "", fmt.Errorf("unsupported build network mode %q", networkMode)
	}

	for line := range strings.SplitSeq(dockerfile, "\n") {
		directive := strings.TrimSpace(line)
		if !strings.HasPrefix(directive, "#") {
			continue
		}
		name, _, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(directive, "#")), "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), "syntax") {
			return "", fmt.Errorf("custom Dockerfile syntax frontends are not allowed")
		}
	}

	parsed, err := parser.Parse(strings.NewReader(dockerfile))
	if err != nil {
		return "", fmt.Errorf("parse dockerfile: %w", err)
	}
	if len(parsed.AST.Children) == 0 {
		return "", fmt.Errorf("dockerfile has no instructions")
	}
	if len(parsed.AST.Children) > maxDockerfileInstructions {
		return "", fmt.Errorf("dockerfile has %d instructions; maximum is %d", len(parsed.AST.Children), maxDockerfileInstructions)
	}

	for _, instruction := range parsed.AST.Children {
		switch strings.ToLower(instruction.Value) {
		case "add":
			return "", fmt.Errorf("ADD is not allowed at line %d; use COPY for local build-context files", instruction.StartLine)
		case "from":
			if instruction.Next == nil {
				return "", fmt.Errorf("FROM has no image at line %d", instruction.StartLine)
			}
			if err := validateImageReference(instruction.Next.Value); err != nil {
				return "", fmt.Errorf("FROM image at line %d: %w", instruction.StartLine, err)
			}
		case "copy":
			for _, flag := range instruction.Flags {
				flag = strings.TrimPrefix(strings.ToLower(flag), "--")
				if source, ok := strings.CutPrefix(flag, "from="); ok {
					if err := validateImageReference(source); err != nil {
						return "", fmt.Errorf("COPY --from at line %d: %w", instruction.StartLine, err)
					}
				}
			}
		case "run":
			for _, flag := range instruction.Flags {
				flag = strings.TrimPrefix(strings.ToLower(flag), "--")
				if flag == "network=host" || flag == "security=insecure" {
					return "", fmt.Errorf("RUN --%s is not allowed at line %d", flag, instruction.StartLine)
				}
			}
		}
	}

	return networkMode, nil
}

func validateImageReference(image string) error {
	image = strings.TrimSpace(strings.ToLower(image))
	if image == "" {
		return fmt.Errorf("image is empty")
	}

	firstComponent, _, hasSlash := strings.Cut(image, "/")
	name := firstComponent
	if colon := strings.IndexByte(name, ':'); colon >= 0 {
		name = name[:colon]
	}
	if strings.ContainsAny(name, "$[]") {
		return fmt.Errorf("variable or address-based image registries are not allowed")
	}
	if !hasSlash {
		return nil
	}
	if !strings.ContainsAny(firstComponent, ".:") && firstComponent != "localhost" {
		return nil
	}
	switch firstComponent {
	case "docker.io", "ghcr.io":
		return nil
	default:
		return fmt.Errorf("registry %q is not allowed", firstComponent)
	}
}
