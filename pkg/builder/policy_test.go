package builder

import (
	"strings"
	"testing"
)

func TestValidateBuildPolicyDefaultsToNoNetwork(t *testing.T) {
	t.Parallel()

	mode, err := validateBuildPolicy("FROM scratch\nCOPY app /app\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if mode != NetworkNone {
		t.Fatalf("network mode = %q, want %q", mode, NetworkNone)
	}
}

func TestValidateBuildPolicyAllowsOfficialDockerfileFrontend(t *testing.T) {
	t.Parallel()

	if _, err := validateBuildPolicy("# syntax=docker/dockerfile:1.10\nFROM scratch\n", NetworkNone); err != nil {
		t.Fatalf("official Dockerfile frontend was rejected: %v", err)
	}
}

func TestDockerfileFrontendAttrsOmitsDefaultNetworkOverride(t *testing.T) {
	t.Parallel()

	if _, ok := dockerfileFrontendAttrs(NetworkDefault, nil)["force-network-mode"]; ok {
		t.Fatal("default network must use BuildKit's native default")
	}
	if got := dockerfileFrontendAttrs(NetworkNone, nil)["force-network-mode"]; got != NetworkNone {
		t.Fatalf("force-network-mode = %q, want %q", got, NetworkNone)
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
			if _, err := validateBuildPolicy(dockerfile, NetworkNone); err == nil {
				t.Fatal("expected policy rejection")
			}
		})
	}
}

func TestValidateBuildPolicyRejectsSpacedSyntaxDirective(t *testing.T) {
	t.Parallel()

	if _, err := validateBuildPolicy("# syntax = evil.example/frontend:1\nFROM alpine\n", NetworkNone); err == nil {
		t.Fatal("expected custom syntax rejection")
	}
}

func TestValidateBuildPolicyRejectsOversizedDockerfile(t *testing.T) {
	t.Parallel()

	dockerfile := "FROM scratch\n#" + strings.Repeat("x", maxDockerfileBytes)
	if _, err := validateBuildPolicy(dockerfile, NetworkNone); err == nil {
		t.Fatal("expected oversized Dockerfile rejection")
	}
}
