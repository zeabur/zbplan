//go:build integration

package plantools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zeabur/zbplan/internal/plantools"
)

// TestBuildPolicyIsEnforcedByBuildKit runs Dockerfiles that previously
// bypassed the string-level policy and checks that the BuildKit daemon itself
// enforces the source policy, entitlements, frontend pin and filtered context.
func TestBuildPolicyIsEnforcedByBuildKit(t *testing.T) {
	ctx := context.Background()
	addr := startBuildkitd(t, ctx)

	contextDir := t.TempDir()
	for name, content := range map[string]string{
		"app.txt":                   "app\n",
		".env":                      "TOKEN=secret\n",
		"config/.npmrc":             "//registry.npmjs.org/:_authToken=secret\n",
		"node_modules/pkg/index.js": "module.exports = 1\n",
		"dist/bundle.js":            "ignored\n",
		".gitignore":                "dist/\n",
	} {
		path := filepath.Join(contextDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const busybox = "busybox:1.37"
	tests := []struct {
		name       string
		registries []string
		dockerfile string
		wantErr    string
	}{
		{
			name: "hidden files are absent from the build context",
			dockerfile: "FROM " + busybox + "\n" +
				"RUN --mount=type=bind,target=/ctx test -f /ctx/app.txt && test ! -e /ctx/.env && " +
				"test ! -e /ctx/config/.npmrc && test ! -e /ctx/node_modules && test ! -e /ctx/dist\n",
		},
		{
			name:       "COPY of a sensitive file fails",
			dockerfile: "FROM scratch\nCOPY .env /env\n",
			wantErr:    ".env",
		},
		{
			name:       "stage references need no registry",
			registries: []string{"ghcr.io"},
			dockerfile: "FROM scratch AS base\nCOPY app.txt /\nFROM base\nCOPY --from=base /app.txt /copy.txt\n",
		},
		{
			name:       "syntax directive cannot select an external frontend",
			registries: []string{"ghcr.io"},
			dockerfile: "# syntax=frontend.invalid/evil:latest\nFROM scratch\nCOPY app.txt /\n",
		},
		{
			name:       "bare COPY --from image follows the allowlist",
			registries: []string{"ghcr.io"},
			dockerfile: "FROM scratch\nCOPY --from=busybox /bin/busybox /busybox\n",
			wantErr:    "denied by policy",
		},
		{
			name:       "RUN --mount from image follows the allowlist",
			dockerfile: "FROM " + busybox + "\nRUN --mount=type=bind,from=registry.invalid/image:latest,target=/m true\n",
			wantErr:    "denied by policy",
		},
		{
			name:       "remote ADD is denied",
			dockerfile: "FROM scratch\nADD https://example.com/ /index.html\n",
			wantErr:    "denied by policy",
		},
		{
			name:       "Git sources are denied",
			dockerfile: "FROM scratch\nADD https://github.com/moby/buildkit.git#v0.29.0 /src\n",
			wantErr:    "denied by policy",
		},
		{
			name:       "host network requires an entitlement",
			dockerfile: "FROM " + busybox + "\nRUN --network=host true\n",
			wantErr:    "network.host",
		},
		{
			name:       "insecure security mode requires an entitlement",
			dockerfile: "FROM " + busybox + "\nRUN --security=insecure true\n",
			wantErr:    "security.insecure",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			bc, err := plantools.NewBuilderClient(ctx, plantools.BuilderClientConfig{
				Addr:              addr,
				ContextDir:        contextDir,
				AllowedRegistries: test.registries,
				Timeout:           5 * time.Minute,
			})
			if err != nil {
				t.Fatalf("connect to buildkit: %v", err)
			}
			t.Cleanup(func() { _ = bc.Close() })

			logs, err := bc.RunBuild(ctx, test.dockerfile, nil)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("build failed:\n%s\nerr: %v", logs, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("build succeeded; want error containing %q", test.wantErr)
			}
			t.Logf("rejected as expected: %v", err)
			if !strings.Contains(err.Error(), test.wantErr) && !strings.Contains(logs, test.wantErr) {
				t.Fatalf("error does not mention %q:\n%s\nerr: %v", test.wantErr, logs, err)
			}
		})
	}
}

// TestBuildTimeoutReturns checks that a build deadline ends the solve and
// returns promptly instead of leaving BuildKit's status stream blocked.
func TestBuildTimeoutReturns(t *testing.T) {
	ctx := context.Background()
	addr := startBuildkitd(t, ctx)

	bc, err := plantools.NewBuilderClient(ctx, plantools.BuilderClientConfig{
		Addr:       addr,
		ContextDir: t.TempDir(),
		Timeout:    20 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect to buildkit: %v", err)
	}
	t.Cleanup(func() { _ = bc.Close() })

	started := time.Now()
	_, err = bc.RunBuild(ctx, "FROM busybox:1.37\nRUN while true; do echo tick; sleep 0.05; done\n", nil)
	if err == nil {
		t.Fatal("expected the build deadline to fail the build")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Minute {
		t.Fatalf("timed-out build returned after %s", elapsed)
	}
}
