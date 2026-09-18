package builder

import (
	"strings"
	"testing"
)

func TestValidateBuildPolicyAllowsOfficialDockerfileFrontend(t *testing.T) {
	t.Parallel()

	if err := validateBuildPolicy("# syntax=docker/dockerfile:1.10\nFROM scratch\n", nil); err != nil {
		t.Fatalf("official Dockerfile frontend was rejected: %v", err)
	}
}

func TestValidateBuildPolicyAllowsTrustedRegistries(t *testing.T) {
	t.Parallel()

	for _, registry := range []string{"docker.io", "ghcr.io", "quay.io", "gcr.io"} {
		t.Run(registry, func(t *testing.T) {
			t.Parallel()
			dockerfile := "FROM " + registry + "/example/image:latest\n"
			if err := validateBuildPolicy(dockerfile, nil); err != nil {
				t.Fatalf("trusted registry was rejected: %v", err)
			}
		})
	}
}

func TestValidateBuildPolicyUsesConfiguredRegistries(t *testing.T) {
	t.Parallel()

	dockerfile := "FROM registry.example.com/team/image:latest AS builder\nCOPY --from=builder /app /app\n"
	if err := validateBuildPolicy(dockerfile, []string{"registry.example.com"}); err != nil {
		t.Fatalf("configured registry was rejected: %v", err)
	}
	if err := validateBuildPolicy("FROM ghcr.io/example/image:latest\n", []string{"registry.example.com"}); err == nil {
		t.Fatal("registry outside configured allowlist was accepted")
	}
}

func TestValidateBuildPolicyRejectsDangerousInstructions(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"custom frontend":         "# syntax=evil.example/frontend:latest\nFROM scratch\n",
		"remote add":              "FROM scratch\nADD https://example.com/payload /payload\n",
		"local add":               "FROM scratch\nADD app /app\n",
		"host network":            "FROM alpine\nRUN --network=host echo unsafe\n",
		"insecure run":            "FROM alpine\nRUN --security=insecure echo unsafe\n",
		"untrusted FROM registry": "FROM metadata.internal/image:latest\n",
		"variable FROM image":     "ARG IMAGE\nFROM ${IMAGE}\n",
		"untrusted COPY registry": "FROM scratch\nCOPY --from=metadata.internal/image /src /dst\n",
		"onbuild remote add":      "FROM scratch\nONBUILD ADD https://example.com/payload /payload\n",
	}
	for name, dockerfile := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateBuildPolicy(dockerfile, nil); err == nil {
				t.Fatal("expected policy rejection")
			}
		})
	}
}

func TestValidateBuildPolicyRejectsSpacedSyntaxDirective(t *testing.T) {
	t.Parallel()

	if err := validateBuildPolicy("# syntax = evil.example/frontend:1\nFROM alpine\n", nil); err == nil {
		t.Fatal("expected custom syntax rejection")
	}
}

func TestValidateBuildPolicyRejectsOversizedDockerfile(t *testing.T) {
	t.Parallel()

	dockerfile := "FROM scratch\n#" + strings.Repeat("x", maxDockerfileBytes)
	if err := validateBuildPolicy(dockerfile, nil); err == nil {
		t.Fatal("expected oversized Dockerfile rejection")
	}
}
