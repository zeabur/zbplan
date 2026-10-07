package builder

import (
	"errors"
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
)

// evaluateSource runs the policy through BuildKit's own source-policy engine,
// the same code buildkitd uses to admit sources during a solve.
func evaluateSource(t *testing.T, policy *spb.Policy, identifier string) error {
	t.Helper()
	engine := sourcepolicy.NewEngine([]*spb.Policy{policy})
	_, err := engine.Evaluate(t.Context(), &pb.SourceOp{Identifier: identifier})
	return err
}

func TestSourcePolicyAllowsBuildInputsAndDefaultRegistries(t *testing.T) {
	t.Parallel()

	policy, err := sourcePolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []string{
		"local://context",
		"local://dockerfile",
		"docker-image://docker.io/library/alpine:latest",
		"docker-image://docker.io/library/alpine:3.22@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"docker-image://ghcr.io/example/image:1",
		"docker-image://quay.io/example/image:1",
		"docker-image://gcr.io/distroless/static:nonroot",
	} {
		if err := evaluateSource(t, policy, identifier); err != nil {
			t.Errorf("%s was denied: %v", identifier, err)
		}
	}
}

func TestSourcePolicyDeniesEverythingElse(t *testing.T) {
	t.Parallel()

	policy, err := sourcePolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []string{
		"docker-image://metadata.internal/image:latest",
		"docker-image://docker.io.evil.example/library/alpine:latest",
		"docker-image://localhost:5000/image:latest",
		"https://example.com/payload",
		"http://169.254.169.254/latest/meta-data",
		"git://github.com/example/repo.git",
		"oci-layout://store/image",
		"local://other",
		"local://context-extra",
	} {
		err := evaluateSource(t, policy, identifier)
		if !errors.Is(err, sourcepolicy.ErrSourceDenied) {
			t.Errorf("%s: expected denial, got %v", identifier, err)
		}
	}
}

func TestSourcePolicyUsesConfiguredRegistries(t *testing.T) {
	t.Parallel()

	policy, err := sourcePolicy([]string{"registry.example.com:5000"})
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluateSource(t, policy, "docker-image://registry.example.com:5000/team/image:latest"); err != nil {
		t.Fatalf("configured registry was denied: %v", err)
	}
	// A bare COPY --from=alpine that names no stage, or the official
	// Dockerfile frontend, resolves to Docker Hub and must follow the list.
	for _, identifier := range []string{
		"docker-image://docker.io/library/alpine:latest",
		"docker-image://docker.io/docker/dockerfile:1",
		"docker-image://ghcr.io/example/image:latest",
	} {
		if err := evaluateSource(t, policy, identifier); !errors.Is(err, sourcepolicy.ErrSourceDenied) {
			t.Errorf("%s: expected denial, got %v", identifier, err)
		}
	}
}

func TestSourcePolicyRejectsInvalidRegistries(t *testing.T) {
	t.Parallel()

	for _, registries := range [][]string{{"*"}, {"evil.example/*"}, {"https://ghcr.io"}} {
		if _, err := sourcePolicy(registries); err == nil {
			t.Errorf("%q: expected error", registries)
		}
	}
}

func TestValidateDockerfileAcceptsBuildKitSyntax(t *testing.T) {
	t.Parallel()

	dockerfile := "# syntax=docker/dockerfile:1\nFROM alpine AS base\nRUN <<EOF\n# syntax=evil.example/frontend\nEOF\nFROM base\nCOPY --from=base / /\n"
	if err := validateDockerfile(dockerfile); err != nil {
		t.Fatalf("valid Dockerfile was rejected: %v", err)
	}
}

func TestValidateDockerfileBoundsParserWork(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"oversized":            "FROM scratch\n#" + strings.Repeat("x", maxDockerfileBytes),
		"too many":             "FROM scratch\n" + strings.Repeat("LABEL a=b\n", maxDockerfileInstructions),
		"too many via onbuild": "FROM scratch\n" + strings.Repeat("ONBUILD LABEL a=b\n", maxDockerfileInstructions/2+1),
		"empty":                "# only a comment\n",
	}
	for name, dockerfile := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateDockerfile(dockerfile); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
