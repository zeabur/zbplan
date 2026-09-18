package plantools

import (
	"context"
	"strings"
	"testing"
)

func TestRunBuildReturnsPolicyDiagnostic(t *testing.T) {
	t.Parallel()

	client := &BuilderClient{
		maxLogBytes: 4 * 1024,
	}
	logs, err := client.RunBuild(context.Background(), "FROM scratch\nADD https://example.com/payload /payload\n", nil)
	if err == nil {
		t.Fatal("expected policy rejection")
	}
	if !strings.Contains(logs, "ADD is not allowed") {
		t.Fatalf("policy diagnostic missing from retry logs: %q", logs)
	}
}

func TestRejectedBuildDoesNotCountSolve(t *testing.T) {
	t.Parallel()

	client := &BuilderClient{
		maxLogBytes: 4 * 1024,
	}
	if _, err := client.RunBuild(context.Background(), "FROM scratch\nADD local /local\n", nil); err == nil {
		t.Fatal("expected policy rejection")
	}
	if got := client.BuildSolves(); got != 0 {
		t.Fatalf("build solves = %d, want 0", got)
	}
}
