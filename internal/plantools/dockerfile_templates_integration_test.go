//go:build integration

package plantools_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/zeabur/zbplan/internal/plantools"
)

// TestTemplatesBuild verifies that every embedded Dockerfile template builds
// successfully against its corresponding fixture project under testdata/fixtures/.
//
// Run with:
//
//	go test -tags=integration -timeout=30m ./internal/plantools/
func TestTemplatesBuild(t *testing.T) {
	ctx := context.Background()
	addr := startBuildkitd(t, ctx)

	for _, tpl := range plantools.Templates() {
		tpl := tpl
		t.Run(tpl.Name, func(t *testing.T) {
			t.Parallel()

			fixtureDir, err := filepath.Abs(filepath.Join("testdata", "fixtures", tpl.Name))
			if err != nil {
				t.Fatalf("resolve fixture path: %v", err)
			}
			if _, err := os.Stat(fixtureDir); err != nil {
				t.Fatalf("fixture missing — create testdata/fixtures/%s/: %v", tpl.Name, err)
			}

			bc, err := plantools.NewBuilderClient(ctx, addr, fixtureDir, nil)
			if err != nil {
				t.Fatalf("connect to buildkit: %v", err)
			}
			t.Cleanup(func() { _ = bc.Close() })

			logs, err := bc.RunBuild(ctx, tpl.Content)
			if err != nil {
				t.Fatalf("build failed:\n%s\nerr: %v", logs, err)
			}
		})
	}
}

func startBuildkitd(t *testing.T, ctx context.Context) string {
	t.Helper()

	// The image ENTRYPOINT is already "buildkitd", so Cmd provides the flags only.
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "moby/buildkit:v0.29.0",
			Privileged:   true,
			ExposedPorts: []string{"1234/tcp"},
			Cmd:          []string{"--addr", "tcp://0.0.0.0:1234"},
			WaitingFor:   wait.ForLog("running server on").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start buildkitd container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "1234/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	return fmt.Sprintf("tcp://%s:%s", host, port.Port())
}
