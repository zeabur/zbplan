package plantools

import (
	"context"
	"strings"
	"testing"
)

func TestRunBuildReturnsPolicyDiagnostic(t *testing.T) {
	t.Parallel()

	client := &BuilderClient{}
	logs, err := client.RunBuild(context.Background(), "FROM scratch\n"+strings.Repeat("LABEL a=b\n", 300), nil)
	if err == nil {
		t.Fatal("expected policy rejection")
	}
	if !strings.Contains(logs, "more than 256 instructions") {
		t.Fatalf("policy diagnostic missing from retry logs: %q", logs)
	}
}
